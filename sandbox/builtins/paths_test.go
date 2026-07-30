package builtins

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_containedPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    string
		member  string
		want    string
		wantErr bool
	}{
		{name: "plain relative name", base: "/work", member: "a/b.txt", want: "/work/a/b.txt"},
		{name: "absolute name is taken as relative", base: "/work", member: "/etc/passwd", want: "/work/etc/passwd"},
		{name: "dotdot cancelled inside base", base: "/work", member: "a/../b.txt", want: "/work/b.txt"},
		{name: "leading dot", base: "/work", member: "./a.txt", want: "/work/a.txt"},
		{name: "trailing slash on base", base: "/work/", member: "a.txt", want: "/work/a.txt"},
		{name: "trailing slash on member", base: "/work", member: "dir/", want: "/work/dir"},
		{name: "empty member is base itself", base: "/work", member: "", want: "/work"},
		{name: "member naming base itself", base: "/work", member: ".", want: "/work"},
		{name: "root base contains everything", base: "/", member: "../../a.txt", want: "/a.txt"},
		{name: "nested base", base: "/work/dest", member: "sub/c.txt", want: "/work/dest/sub/c.txt"},

		{name: "dotdot escapes", base: "/work", member: "../a.txt", wantErr: true},
		{name: "dotdot escapes behind a prefix", base: "/work", member: "sub/../../a.txt", wantErr: true},
		{name: "dotdot escapes past root", base: "/work", member: "../../../../a.txt", wantErr: true},
		{name: "absolute name with dotdot escapes", base: "/work", member: "/../a.txt", wantErr: true},
		{name: "sibling prefix is not containment", base: "/work", member: "../workshop/a.txt", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := containedPath(tt.base, tt.member)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.member)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
