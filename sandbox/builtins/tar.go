package builtins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/spf13/afero"
)

// tarCommand creates, extracts, or lists tar archives, optionally gzip-filtered.
// Members are stored with names relative to the working directory (or -C dir).
//
//	-c create / -x extract / -t list (choose exactly one)
//	-z filter through gzip / -f FILE archive path (default stdin/stdout)
//	-C DIR change to DIR before creating or extracting / -v list names processed
//	tar -c[z]f archive file...
//	tar -x[z]f archive [-C dir]
//	tar -t[z]f archive
func tarCommand(_ context.Context, inv *Invocation) error {
	fs := NewFlagSet()
	create := fs.Bool("-c")
	extract := fs.Bool("-x")
	list := fs.Bool("-t")
	gzFlag := fs.Bool("-z")
	verboseFlag := fs.Bool("-v")
	archiveFlag := fs.String("", "-f")
	changeDirFlag := fs.String("", "-C")
	files, err := fs.Parse(inv.Args)
	if err != nil {
		return err
	}
	gz := *gzFlag
	verbose := *verboseFlag
	archive := *archiveFlag
	changeDir := *changeDirFlag

	// Exactly one of -c/-x/-t selects the mode.
	var mode byte
	for _, m := range []struct {
		set bool
		c   byte
	}{{*create, 'c'}, {*extract, 'x'}, {*list, 't'}} {
		if !m.set {
			continue
		}
		if mode != 0 {
			return fmt.Errorf("conflicting modes -- %c and %c", mode, m.c)
		}
		mode = m.c
	}
	if mode == 0 {
		return fmt.Errorf("usage: tar -c|-x|-t [-z] -f archive [file...]")
	}

	base := inv.Dir
	if changeDir != "" {
		base = inv.Abs(changeDir)
	}

	switch mode {
	case 'c':
		return tarCreate(inv, archive, base, gz, verbose, files)
	case 'x':
		return tarExtract(inv, archive, base, gz, verbose)
	case 't':
		return tarList(inv, archive, gz)
	}
	return nil
}

func tarCreate(inv *Invocation, archive, base string, gz, verbose bool, files []string) error {
	if archive == "" {
		return fmt.Errorf("no archive file specified")
	}
	if len(files) == 0 {
		return fmt.Errorf("no files specified")
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	guard := &walkGuard{inv: inv}
	for _, f := range files {
		root := resolveUnder(base, f)
		err := afero.Walk(inv.FS, root, guard.wrap(func(p string, info os.FileInfo, _ error) error {
			// A link cannot be stored as a link: the archive would carry a member
			// that extraction has no way to recreate, since the virtual filesystem
			// offers no link creation. Storing the target's bytes under the link's
			// name instead would put a file in the archive that the tree does not
			// have, so the member is reported and left out. GNU tar dereferences
			// only with -h, which is not implemented.
			if info.Mode()&os.ModeSymlink != 0 {
				guard.report(fmt.Errorf("%s: symbolic link not archived", p))
				return nil
			}
			// Name members after the typed argument (GNU semantics), then drop
			// any leading slash so absolute arguments never write absolute
			// members into the archive.
			name := strings.TrimPrefix(walkedName(f, root, p), "/")
			if name == "" {
				return nil
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = name
			if info.IsDir() {
				hdr.Name += "/"
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if verbose {
				fmt.Fprintln(inv.Stdout, hdr.Name)
			}
			if info.IsDir() {
				return nil
			}
			b, err := afero.ReadFile(inv.FS, p)
			if err != nil {
				return err
			}
			_, err = tw.Write(b)
			return err
		}))
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}

	out := buf.Bytes()
	if gz {
		var gzBuf bytes.Buffer
		if err := writeGzip(&gzBuf, out); err != nil {
			return err
		}
		out = gzBuf.Bytes()
	}
	// The archive is written even when a member was refused: it holds everything
	// that could be read, and the exit code reports that it is not the whole tree.
	if err := afero.WriteFile(inv.FS, inv.Abs(archive), out, 0o644); err != nil {
		return err
	}
	if guard.refused {
		return exit(2)
	}
	return nil
}

// tarExtract writes every member under base. Member names are untrusted: an
// absolute name is extracted relative to base, and a name climbing out of base
// with ".." aborts the extraction.
//
// Members are validated as they are read, not up front — a tar stream would
// have to be buffered whole to check it in advance — so an abort leaves the
// members written before it in place.
func tarExtract(inv *Invocation, archive, base string, gz, verbose bool) error {
	tr, closer, err := tarReader(inv, archive, gz)
	if err != nil {
		return err
	}
	defer closer()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, err := containedPath(base, hdr.Name)
		if err != nil {
			return err
		}
		if verbose {
			fmt.Fprintln(inv.Stdout, hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := inv.FS.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		default:
			if err := inv.FS.MkdirAll(path.Dir(target), 0o755); err != nil {
				return err
			}
			b, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if err := afero.WriteFile(inv.FS, target, b, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		}
	}
	return nil
}

func tarList(inv *Invocation, archive string, gz bool) error {
	tr, closer, err := tarReader(inv, archive, gz)
	if err != nil {
		return err
	}
	defer closer()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(inv.Stdout, hdr.Name)
	}
	return nil
}

// tarReader opens the archive (file or stdin) and returns a tar.Reader plus a
// closer for any gzip stream it wraps.
func tarReader(inv *Invocation, archive string, gz bool) (*tar.Reader, func(), error) {
	b, err := readSource(inv, archive)
	if err != nil {
		return nil, nil, err
	}
	var r io.Reader = bytes.NewReader(b)
	closer := func() {}
	if gz {
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, err
		}
		r = gr
		closer = func() { gr.Close() }
	}
	return tar.NewReader(r), closer, nil
}

// resolveUnder joins p onto base unless p is already absolute. p must come from
// a user argument: the result is not guaranteed to stay under base, so untrusted
// names go through containedPath instead.
func resolveUnder(base, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Clean(path.Join(base, p))
}

func init() {
	Register("tar", tarCommand)
}
