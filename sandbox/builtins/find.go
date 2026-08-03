package builtins

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// find walks the given paths recursively and prints paths matching the conditions.
//
//	-name PATTERN  base name matches the glob / -type f|d  filter by file kind
//	find [path...] [-name pattern] [-type f|d]
func find(_ context.Context, inv *command.Invocation) error {
	var roots []string
	var namePat, typ string
	for i := 0; i < len(inv.Args); i++ {
		a := inv.Args[i]
		switch a {
		case "-name":
			i++
			if i >= len(inv.Args) {
				return fmt.Errorf("-name requires an argument")
			}
			namePat = inv.Args[i]
		case "-type":
			i++
			if i >= len(inv.Args) {
				return fmt.Errorf("-type requires an argument")
			}
			typ = inv.Args[i]
			if typ != "f" && typ != "d" {
				return fmt.Errorf("invalid -type %q (want f or d)", typ)
			}
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown option: %s", a)
			}
			roots = append(roots, a)
		}
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}

	guard := &walkGuard{inv: inv}
	for _, root := range roots {
		abs := inv.Abs(root)
		err := afero.Walk(inv.FS, abs, guard.wrap(func(p string, info os.FileInfo, _ error) error {
			if typ == "f" && info.IsDir() {
				return nil
			}
			if typ == "d" && !info.IsDir() {
				return nil
			}
			if namePat != "" {
				ok, err := path.Match(namePat, path.Base(p))
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			fmt.Fprintln(inv.Stdout, walkedName(root, abs, p))
			return nil
		}))
		if err != nil {
			return err
		}
	}

	if guard.refused {
		return exit(1)
	}
	return nil
}

func init() {
	Register("find", find)
}
