package builtins

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_seq(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		args    []string
		want    string
		wantErr bool
	}{
		"single last":                   {args: []string{"3"}, want: "1\n2\n3\n"},
		"first and last":                {args: []string{"2", "5"}, want: "2\n3\n4\n5\n"},
		"with step":                     {args: []string{"1", "2", "7"}, want: "1\n3\n5\n7\n"},
		"negative step counts down":     {args: []string{"3", "-1", "1"}, want: "3\n2\n1\n"},
		"custom separator":              {args: []string{"-s", ",", "3"}, want: "1,2,3\n"},
		"attached separator":            {args: []string{"-s,", "3"}, want: "1,2,3\n"},
		"float step keeps precision":    {args: []string{"1", "0.5", "2"}, want: "1.0\n1.5\n2.0\n"},
		"equal width pads zeros":        {args: []string{"-w", "8", "10"}, want: "08\n09\n10\n"},
		"empty when first exceeds last": {args: []string{"5", "1"}, want: ""},
		"no args errors":                {args: nil, wantErr: true},
		"zero step errors":              {args: []string{"1", "0", "5"}, wantErr: true},
		"non-numeric errors":            {args: []string{"x"}, wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			env, stdout, _ := NewTestEnv(t, "/work")
			err := seq(context.Background(), env, tc.args)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}
