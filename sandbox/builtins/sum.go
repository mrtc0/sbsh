package builtins

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"fmt"
	"hash"
)

// hashFiles computes a checksum over each input using newHash and prints it in
// the GNU coreutils format "<hex>  <name>". With no files it reads stdin and
// reports the name "-".
func hashFiles(env *Env, args []string, newHash func() hash.Hash) error {
	files, err := NewFlagSet().Parse(args)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	for _, f := range files {
		b, err := readSource(env, f)
		if err != nil {
			return err
		}
		h := newHash()
		h.Write(b)
		name := f
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(env.HC.Stdout, "%x  %s\n", h.Sum(nil), name)
	}
	return nil
}

func md5sum(_ context.Context, env *Env, args []string) error {
	return hashFiles(env, args, func() hash.Hash { return md5.New() })
}

func sha1sum(_ context.Context, env *Env, args []string) error {
	return hashFiles(env, args, func() hash.Hash { return sha1.New() })
}

func sha256sum(_ context.Context, env *Env, args []string) error {
	return hashFiles(env, args, func() hash.Hash { return sha256.New() })
}

func init() {
	Register("md5sum", md5sum)
	Register("sha1sum", sha1sum)
	Register("sha256sum", sha256sum)
}
