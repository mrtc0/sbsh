package builtins

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// base64Command encodes or decodes data with standard base64. With no file it
// reads stdin; a single file argument is read instead.
//
//	-d decode / -w N wrap encoded output at N columns (0 disables wrapping)
//	base64 [-d] [-w N] [file]
func base64Command(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	decodeFlag := fs.Bool("-d", "--decode")
	wrapFlag := fs.String("76", "-w", "--wrap")
	files, err := fs.Parse(inv.Args)
	if err != nil {
		return err
	}
	decode := *decodeFlag
	wrap, err := strconv.Atoi(*wrapFlag)
	if err != nil {
		return fmt.Errorf("invalid wrap size: %q", *wrapFlag)
	}
	if len(files) > 1 {
		return fmt.Errorf("usage: base64 [-d] [-w N] [file]")
	}
	src := "-"
	if len(files) == 1 {
		src = files[0]
	}
	b, err := readSource(inv, src)
	if err != nil {
		return err
	}

	if decode {
		// Ignore any whitespace (including newlines) in the encoded input.
		clean := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(b))
		out, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return fmt.Errorf("invalid input: %w", err)
		}
		_, err = inv.Stdout.Write(out)
		return err
	}

	encoded := base64.StdEncoding.EncodeToString(b)
	if wrap <= 0 {
		_, err := fmt.Fprintln(inv.Stdout, encoded)
		return err
	}
	for i := 0; i < len(encoded); i += wrap {
		end := i + wrap
		if end > len(encoded) {
			end = len(encoded)
		}
		if _, err := fmt.Fprintln(inv.Stdout, encoded[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	Register("base64", base64Command)
}
