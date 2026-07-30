package builtins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/itchyny/gojq"
)

// jqCommand filters JSON with a gojq (pure-Go jq) program. With no files it
// reads stdin; multiple files are treated as one concatenated JSON stream.
//
//	-c compact output / -r raw string output / -n null input
//	-s slurp all inputs into a single array / -e set exit status from the last output
//	jq [-cnrse] filter [file...]
func jqCommand(ctx context.Context, env *Env, args []string) error {
	fs := NewFlagSet()
	compactFlag := fs.Bool("-c", "--compact-output")
	rawFlag := fs.Bool("-r", "--raw-output")
	nullInputFlag := fs.Bool("-n", "--null-input")
	slurpFlag := fs.Bool("-s", "--slurp")
	exitStatusFlag := fs.Bool("-e", "--exit-status")
	rest, err := fs.Parse(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: jq [-cnrse] filter [file...]")
	}
	filter := rest[0]
	files := rest[1:]

	compact := *compactFlag
	raw := *rawFlag
	nullInput := *nullInputFlag
	slurp := *slurpFlag
	exitStatus := *exitStatusFlag

	query, err := gojq.Parse(filter)
	if err != nil {
		return fmt.Errorf("compile error: %w", err)
	}

	inputs, err := jqInputs(env, files, nullInput, slurp)
	if err != nil {
		return err
	}

	// exit-status tracking: 0 if the last output was neither null nor false,
	// 1 if it was, and 4 when the filter produced no output at all.
	var lastOutput any
	produced := false

	for _, input := range inputs {
		iter := query.RunWithContext(ctx, input)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if e, ok := v.(error); ok {
				var halt *gojq.HaltError
				if errors.As(e, &halt) {
					if code := halt.ExitCode(); code != 0 {
						return exit(code)
					}
					return nil
				}
				return e
			}
			produced = true
			lastOutput = v
			if err := jqWrite(env.HC.Stdout, v, compact, raw); err != nil {
				return err
			}
		}
	}

	if exitStatus {
		switch {
		case !produced:
			return exit(4)
		case lastOutput == nil || lastOutput == false:
			return exit(1)
		}
	}
	return nil
}

// jqInputs collects the decoded JSON values the filter runs over. With
// nullInput it is a single nil; otherwise the input stream is decoded into one
// value per top-level JSON document, or a single array when slurping.
func jqInputs(env *Env, files []string, nullInput, slurp bool) ([]any, error) {
	if nullInput {
		return []any{nil}, nil
	}

	var raw []byte
	if len(files) == 0 {
		b, err := readSource(env, "-")
		if err != nil {
			return nil, err
		}
		raw = b
	} else {
		for _, f := range files {
			b, err := readSource(env, f)
			if err != nil {
				return nil, err
			}
			raw = append(raw, b...)
			raw = append(raw, '\n')
		}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var values []any
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		values = append(values, normalizeJSON(v))
	}

	if slurp {
		return []any{values}, nil
	}
	return values, nil
}

// normalizeJSON converts json.Number values into the int/float form gojq
// expects, since gojq operates on Go's native numeric types rather than
// json.Number.
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeJSON(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeJSON(val)
		}
		return t
	default:
		return v
	}
}

// jqWrite renders one filter result. Strings under -r are printed verbatim;
// everything else is JSON-encoded, pretty-printed unless compact.
func jqWrite(w io.Writer, v any, compact, raw bool) error {
	if raw {
		if s, ok := v.(string); ok {
			_, err := fmt.Fprintln(w, s)
			return err
		}
	}

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if !compact {
		enc.SetIndent("", "  ")
	}
	// json.Encoder appends its own trailing newline.
	return enc.Encode(v)
}

func init() {
	Register("jq", jqCommand)
}
