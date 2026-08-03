package builtins

import (
	"context"
)

func cat(_ context.Context, env *Env) error {
	paths, err := NewFlagSet().Parse(env.Args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		paths = []string{"-"}
	}
	for _, p := range paths {
		b, err := readSource(env, p)
		if err != nil {
			return err
		}
		if _, err := env.Stdout.Write(b); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	Register("cat", cat)
}
