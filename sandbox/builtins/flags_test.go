package builtins

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_FlagSet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// build configures a FlagSet and returns a checker that asserts the
		// flag pointer values once Parse succeeds (nil when there is nothing
		// to assert beyond the operands and error).
		build func() (*FlagSet, func(t *testing.T))
		args  []string
		// wantOperands is compared when non-nil; otherwise the operands are
		// expected to be empty.
		wantOperands []string
		wantErr      bool
	}{
		{
			name: "short: grouped booleans",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				r := fs.Bool("-r")
				n := fs.Bool("-n")
				return fs, func(t *testing.T) {
					assert.True(t, *r)
					assert.True(t, *n)
				}
			},
			args:         []string{"-rn", "file"},
			wantOperands: []string{"file"},
		},
		{
			name: "short: attached value",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				n := fs.String("", "-n")
				return fs, func(t *testing.T) { assert.Equal(t, "5", *n) }
			},
			args:         []string{"-n5", "file"},
			wantOperands: []string{"file"},
		},
		{
			name: "short: separated value taken verbatim even if dash-prefixed",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				n := fs.String("", "-n")
				return fs, func(t *testing.T) { assert.Equal(t, "-5", *n) }
			},
			args: []string{"-n", "-5"},
		},
		{
			name: "short: value flag consumes rest of group",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				v := fs.Bool("-v")
				f := fs.String("", "-f")
				return fs, func(t *testing.T) {
					assert.True(t, *v)
					assert.Equal(t, "script.awk", *f)
				}
			},
			args: []string{"-vfscript.awk"},
		},
		{
			name: "short: missing value errors",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				fs.String("", "-f")
				return fs, nil
			},
			args:    []string{"-f"},
			wantErr: true,
		},
		{
			name: "short: unknown flag errors",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				fs.Bool("-r")
				return fs, nil
			},
			args:    []string{"-x"},
			wantErr: true,
		},
		{
			name: "long: name=value",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				out := fs.String("", "-o", "--output")
				return fs, func(t *testing.T) { assert.Equal(t, "x", *out) }
			},
			args: []string{"--output=x"},
		},
		{
			name: "long: name value",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				out := fs.String("", "--output")
				return fs, func(t *testing.T) { assert.Equal(t, "x", *out) }
			},
			args: []string{"--output", "x"},
		},
		{
			name: "long: bool with value errors",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				fs.Bool("--verbose")
				return fs, nil
			},
			args:    []string{"--verbose=1"},
			wantErr: true,
		},
		{
			name: "long: unknown flag errors",
			build: func() (*FlagSet, func(*testing.T)) {
				return NewFlagSet(), nil
			},
			args:    []string{"--nope"},
			wantErr: true,
		},
		{
			name: "operands: dash is an operand",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				fs.Bool("-r")
				return fs, nil
			},
			args:         []string{"-r", "-"},
			wantOperands: []string{"-"},
		},
		{
			name: "operands: double dash ends options",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				r := fs.Bool("-r")
				return fs, func(t *testing.T) { assert.True(t, *r) }
			},
			args:         []string{"-r", "--", "-notaflag"},
			wantOperands: []string{"-notaflag"},
		},
		{
			name: "operands: GNU permutation keeps flags after operands",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				r := fs.Bool("-r")
				return fs, func(t *testing.T) { assert.True(t, *r) }
			},
			args:         []string{"file", "-r", "other"},
			wantOperands: []string{"file", "other"},
		},
		{
			name: "operands: StopAtFirstOperand keeps trailing dashes as operands",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet().StopAtFirstOperand()
				v := fs.Bool("-v")
				return fs, func(t *testing.T) { assert.True(t, *v) }
			},
			args:         []string{"-v", "prog", "-x", "file"},
			wantOperands: []string{"prog", "-x", "file"},
		},
		{
			name: "operands: AllowNegativeOperands treats -3 as an operand",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet().AllowNegativeOperands()
				sep := fs.String("\n", "-s")
				return fs, func(t *testing.T) { assert.Equal(t, ",", *sep) }
			},
			args:         []string{"-s", ",", "-3", "3"},
			wantOperands: []string{"-3", "3"},
		},
		{
			name: "stringList: each occurrence appends",
			build: func() (*FlagSet, func(*testing.T)) {
				fs := NewFlagSet()
				e := fs.StringList("-e")
				return fs, func(t *testing.T) {
					assert.Equal(t, []string{"one", "two", "three"}, *e)
				}
			},
			args: []string{"-e", "one", "-e", "two", "-ethree"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs, check := tc.build()
			operands, err := fs.Parse(tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if check != nil {
				check(t)
			}
			if tc.wantOperands != nil {
				assert.Equal(t, tc.wantOperands, operands)
			} else {
				assert.Empty(t, operands)
			}
		})
	}
}

func Test_parseLineCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		args      []string
		def       int
		wantN     int
		wantFiles []string
		wantErr   bool
	}{
		{
			name:      "uses the default when -n is absent",
			args:      []string{"a", "b"},
			def:       10,
			wantN:     10,
			wantFiles: []string{"a", "b"},
		},
		{
			name:      "separated value",
			args:      []string{"-n", "3", "file"},
			def:       10,
			wantN:     3,
			wantFiles: []string{"file"},
		},
		{
			name:      "attached value",
			args:      []string{"-n5", "file"},
			def:       10,
			wantN:     5,
			wantFiles: []string{"file"},
		},
		{
			name:      "dash-prefixed value is taken verbatim",
			args:      []string{"-n", "-5", "file"},
			def:       10,
			wantN:     -5,
			wantFiles: []string{"file"},
		},
		{
			name:  "no operands returns an empty file list",
			args:  []string{"-n", "2"},
			def:   10,
			wantN: 2,
		},
		{
			name:    "non-numeric value errors",
			args:    []string{"-n", "abc"},
			def:     10,
			wantErr: true,
		},
		{
			name:    "missing -n value propagates the parse error",
			args:    []string{"-n"},
			def:     10,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, files, err := parseLineCount(tc.args, tc.def)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantN, n)
			if tc.wantFiles != nil {
				assert.Equal(t, tc.wantFiles, files)
			} else {
				assert.Empty(t, files)
			}
		})
	}
}

func Test_readSource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		seed     map[string]string
		setStdin bool
		stdin    string
		path     string
		want     string
		wantNil  bool
		wantErr  bool
	}{
		{
			name: "reads a relative path against the working directory",
			seed: map[string]string{"/work/f": "hello"},
			path: "f",
			want: "hello",
		},
		{
			name: "reads an absolute path",
			seed: map[string]string{"/etc/data": "world"},
			path: "/etc/data",
			want: "world",
		},
		{
			name:     "empty path reads stdin",
			setStdin: true,
			stdin:    "from stdin",
			path:     "",
			want:     "from stdin",
		},
		{
			name:     "dash reads stdin",
			setStdin: true,
			stdin:    "from stdin",
			path:     "-",
			want:     "from stdin",
		},
		{
			name:    "nil stdin yields no content",
			path:    "-",
			wantNil: true,
		},
		{
			name:    "missing file errors",
			path:    "nope",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, _, _ := NewTestEnv(t, "/work")
			for path, body := range tc.seed {
				mustWrite(t, env.FS, path, body)
			}
			if tc.setStdin {
				env.Stdin = strings.NewReader(tc.stdin)
			}

			got, err := readSource(env, tc.path)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantNil {
				assert.Nil(t, got)
				return
			}
			assert.Equal(t, tc.want, string(got))
		})
	}
}
