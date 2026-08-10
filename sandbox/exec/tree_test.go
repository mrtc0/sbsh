package exec_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mrtc0/sbsh/sandbox/exec"
)

func TestExecutionIDCarriesTheAncestry(t *testing.T) {
	t.Parallel()

	root := exec.Root()
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, root.ID,
		"a root is named by a UUID, so it is still unique outside the sandbox that ran it")
	assert.Equal(t, 0, root.Depth)

	child := root.Child(1)
	assert.Equal(t, root.ID+".1", child.ID)
	assert.Equal(t, 1, child.Depth)

	grandchild := child.Child(2)
	assert.Equal(t, root.ID+".1.2", grandchild.ID)
	assert.Equal(t, 2, grandchild.Depth)
}

func TestRootIDsAreNotReused(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, 100)
	for range 100 {
		id := exec.Root().ID
		assert.False(t, seen[id], "two roots were named the same")
		seen[id] = true
	}
}

func TestExecutionTravelsInTheContext(t *testing.T) {
	t.Parallel()

	// A context from outside the sandbox carries no execution, which is what
	// tells a top-level call from a re-entrant one.
	_, ok := exec.FromContext(context.Background())
	assert.False(t, ok)

	want := exec.Root().Child(2)
	got, ok := exec.FromContext(exec.NewContext(context.Background(), want))
	assert.True(t, ok)
	assert.Equal(t, want, got)
}
