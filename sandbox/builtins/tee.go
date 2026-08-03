package builtins

import (
	"context"
	"os"
)

// tee copies stdin to stdout and to each named file.
//
//	-a append to the files instead of truncating them
//	tee [-a] file...
func tee(_ context.Context, env *Env) error {
	fs := NewFlagSet()
	appendMode := fs.Bool("-a", "--append")
	files, err := fs.Parse(env.Args)
	if err != nil {
		return err
	}

	b, err := readSource(env, "-")
	if err != nil {
		return err
	}

	if _, err := env.Stdout.Write(b); err != nil {
		return err
	}

	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if *appendMode {
		flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	for _, f := range files {
		file, err := env.FS.OpenFile(env.Abs(f), flag, 0o644)
		if err != nil {
			return err
		}
		if _, err := file.Write(b); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	Register("tee", tee)
}
