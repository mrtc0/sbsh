package builtins

import (
	"context"
	"os"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// tee copies stdin to stdout and to each named file.
//
//	-a append to the files instead of truncating them
//	tee [-a] file...
func tee(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	appendMode := fs.Bool("-a", "--append")
	files, err := fs.Parse(inv.Args)
	if err != nil {
		return err
	}

	b, err := readSource(inv, "-")
	if err != nil {
		return err
	}

	if _, err := inv.Stdout.Write(b); err != nil {
		return err
	}

	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if *appendMode {
		flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	for _, f := range files {
		file, err := inv.FS.OpenFile(inv.Abs(f), flag, 0o644)
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
