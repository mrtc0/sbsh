package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlags_options(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		flags   flags
		wantLen int
		wantErr string // asserted with Contains when non-empty
	}{
		"no flags means no options": {
			flags: flags{},
		},
		"a read-write mount": {
			flags:   flags{mounts: []string{"/host/dir:/work"}},
			wantLen: 1,
		},
		"a read-only mount": {
			flags:   flags{mounts: []string{"/host/dir:/work:ro"}},
			wantLen: 1,
		},
		"deny paths become one option however many are given": {
			flags:   flags{denyPaths: []string{"**/.env", "/work/secrets"}},
			wantLen: 1,
		},
		"allowed destinations become one option": {
			flags:   flags{allowNet: []string{"example.com", "10.0.1.1/24"}},
			wantLen: 1,
		},
		"a timeout becomes one option": {
			flags:   flags{timeout: "1m"},
			wantLen: 1,
		},
		"a zero timeout is still an option, and lifts the deadline": {
			flags:   flags{timeout: "0"},
			wantLen: 1,
		},
		"an output limit becomes one option": {
			flags:   flags{outputLimit: "1048576"},
			wantLen: 1,
		},
		"every kind of flag together": {
			flags: flags{
				mounts:      []string{"/host/dir:/work"},
				denyPaths:   []string{"**/.env"},
				allowNet:    []string{"example.com"},
				timeout:     "500ms",
				outputLimit: "1048576",
			},
			wantLen: 5,
		},
		"a mount without a virtual path is rejected": {
			flags:   flags{mounts: []string{"/host/dir"}},
			wantErr: "invalid --mount",
		},
		"an unknown mount qualifier is rejected": {
			flags:   flags{mounts: []string{"/host/dir:/work:rw"}},
			wantErr: "invalid --mount",
		},
		"a timeout that is not a duration is rejected": {
			flags:   flags{timeout: "30"},
			wantErr: `invalid --timeout "30": want a duration`,
		},
		"a negative timeout is rejected": {
			flags:   flags{timeout: "-1s"},
			wantErr: "cannot be negative",
		},
		"an output limit that is not a number is rejected": {
			flags:   flags{outputLimit: "1MiB"},
			wantErr: `invalid --output-limit "1MiB": want a whole number of bytes`,
		},
		"a zero output limit is rejected": {
			flags:   flags{outputLimit: "0"},
			wantErr: "must be greater than zero",
		},
		"a negative output limit is rejected": {
			flags:   flags{outputLimit: "-1"},
			wantErr: "must be greater than zero",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts, err := tc.flags.options()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, opts, tc.wantLen)
		})
	}
}
