package builtins

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// gzipCommand compresses files with gzip. Without files it reads stdin and
// writes the compressed stream to stdout. For each file it writes "<file>.gz"
// and removes the original unless -k or -c is given.
//
//	-c write to stdout and keep inputs / -d decompress / -k keep input files
//	gzip [-cdk] [file...]
func gzipCommand(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	decompress := fs.Bool("-d", "--decompress")
	stdout := fs.Bool("-c", "--stdout")
	keepFlag := fs.Bool("-k", "--keep")
	files, err := fs.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	if *decompress {
		return gunzipFiles(inv, files, *stdout, *keepFlag)
	}
	toStdout := *stdout
	keep := *keepFlag

	if len(files) == 0 {
		b, err := readSource(inv, "-")
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if err := writeGzip(inv.Stdout, b); err != nil {
			return command.Exitf(1, "%v", err)
		}
		return nil
	}

	for _, f := range files {
		abs := inv.Abs(f)
		b, err := afero.ReadFile(inv.FS, abs)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if toStdout {
			if err := writeGzip(inv.Stdout, b); err != nil {
				return command.Exitf(1, "%v", err)
			}
			continue
		}
		var buf bytes.Buffer
		if err := writeGzip(&buf, b); err != nil {
			return command.Exitf(1, "%v", err)
		}
		if err := afero.WriteFile(inv.FS, abs+".gz", buf.Bytes(), 0o644); err != nil {
			return command.Exitf(1, "%v", err)
		}
		if !keep {
			if err := inv.FS.Remove(abs); err != nil {
				return command.Exitf(1, "%v", err)
			}
		}
	}
	return nil
}

// gunzipCommand decompresses gzip files. Without files it reads stdin and
// writes to stdout. For each "<name>.gz" file it writes "<name>" and removes
// the archive unless -k or -c is given.
//
//	gunzip [-ck] [file...]
func gunzipCommand(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	stdout := fs.Bool("-c", "--stdout")
	keep := fs.Bool("-k", "--keep")
	files, err := fs.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	return gunzipFiles(inv, files, *stdout, *keep)
}

// zcatCommand decompresses gzip files to stdout, always keeping the inputs.
//
//	zcat [file...]
func zcatCommand(_ context.Context, inv *command.Invocation) error {
	files, err := NewFlagSet().Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	return gunzipFiles(inv, files, true, true)
}

func gunzipFiles(inv *command.Invocation, files []string, toStdout, keep bool) error {
	if len(files) == 0 {
		b, err := readSource(inv, "-")
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		out, err := readGzip(b)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if _, err := inv.Stdout.Write(out); err != nil {
			return command.Exitf(1, "%v", err)
		}
		return nil
	}

	for _, f := range files {
		abs := inv.Abs(f)
		b, err := afero.ReadFile(inv.FS, abs)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		out, err := readGzip(b)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if toStdout {
			if _, err := inv.Stdout.Write(out); err != nil {
				return command.Exitf(1, "%v", err)
			}
			continue
		}
		if !strings.HasSuffix(abs, ".gz") {
			return command.Exitf(1, "%s: unknown suffix -- ignored", f)
		}
		target := strings.TrimSuffix(abs, ".gz")
		if err := afero.WriteFile(inv.FS, target, out, 0o644); err != nil {
			return command.Exitf(1, "%v", err)
		}
		if !keep {
			if err := inv.FS.Remove(abs); err != nil {
				return command.Exitf(1, "%v", err)
			}
		}
	}
	return nil
}

func writeGzip(w io.Writer, b []byte) error {
	gz := gzip.NewWriter(w)
	if _, err := gz.Write(b); err != nil {
		return err
	}
	return gz.Close()
}

func readGzip(b []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
}

func init() {
	Register("gzip", gzipCommand)
	Register("gunzip", gunzipCommand)
	Register("zcat", zcatCommand)
}
