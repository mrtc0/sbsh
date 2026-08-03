package builtins

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/afero"
)

// gzipCommand compresses files with gzip. Without files it reads stdin and
// writes the compressed stream to stdout. For each file it writes "<file>.gz"
// and removes the original unless -k or -c is given.
//
//	-c write to stdout and keep inputs / -d decompress / -k keep input files
//	gzip [-cdk] [file...]
func gzipCommand(_ context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	decompress := fs.Bool("-d", "--decompress")
	stdout := fs.Bool("-c", "--stdout")
	keepFlag := fs.Bool("-k", "--keep")
	files, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if *decompress {
		return gunzipFiles(env, files, *stdout, *keepFlag)
	}
	toStdout := *stdout
	keep := *keepFlag

	if len(files) == 0 {
		b, err := readSource(env, "-")
		if err != nil {
			return err
		}
		return writeGzip(env.Stdout, b)
	}

	for _, f := range files {
		abs := env.Abs(f)
		b, err := afero.ReadFile(env.FS, abs)
		if err != nil {
			return err
		}
		if toStdout {
			if err := writeGzip(env.Stdout, b); err != nil {
				return err
			}
			continue
		}
		var buf bytes.Buffer
		if err := writeGzip(&buf, b); err != nil {
			return err
		}
		if err := afero.WriteFile(env.FS, abs+".gz", buf.Bytes(), 0o644); err != nil {
			return err
		}
		if !keep {
			if err := env.FS.Remove(abs); err != nil {
				return err
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
func gunzipCommand(_ context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	stdout := fs.Bool("-c", "--stdout")
	keep := fs.Bool("-k", "--keep")
	files, err := fs.Parse(args)
	if err != nil {
		return err
	}
	return gunzipFiles(env, files, *stdout, *keep)
}

// zcatCommand decompresses gzip files to stdout, always keeping the inputs.
//
//	zcat [file...]
func zcatCommand(_ context.Context, env *Env, args []string) error {
	files, err := NewFlagSet().Parse(args)
	if err != nil {
		return err
	}
	return gunzipFiles(env, files, true, true)
}

func gunzipFiles(env *Env, files []string, toStdout, keep bool) error {
	if len(files) == 0 {
		b, err := readSource(env, "-")
		if err != nil {
			return err
		}
		out, err := readGzip(b)
		if err != nil {
			return err
		}
		_, err = env.Stdout.Write(out)
		return err
	}

	for _, f := range files {
		abs := env.Abs(f)
		b, err := afero.ReadFile(env.FS, abs)
		if err != nil {
			return err
		}
		out, err := readGzip(b)
		if err != nil {
			return err
		}
		if toStdout {
			if _, err := env.Stdout.Write(out); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(abs, ".gz") {
			return fmt.Errorf("%s: unknown suffix -- ignored", f)
		}
		target := strings.TrimSuffix(abs, ".gz")
		if err := afero.WriteFile(env.FS, target, out, 0o644); err != nil {
			return err
		}
		if !keep {
			if err := env.FS.Remove(abs); err != nil {
				return err
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
