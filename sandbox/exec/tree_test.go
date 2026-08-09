package exec_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mrtc0/sbsh/sandbox/exec"
)

func TestExecutionIDCarriesTheAncestry(t *testing.T) {
	t.Parallel()

	root := exec.Root(3)
	assert.Equal(t, "3", root.ID)
	assert.Equal(t, 0, root.Depth)

	child := root.Child(5)
	assert.Equal(t, "3.5", child.ID)
	assert.Equal(t, 1, child.Depth)

	grandchild := child.Child(6)
	assert.Equal(t, "3.5.6", grandchild.ID)
	assert.Equal(t, 2, grandchild.Depth)
}

func TestExecutionTravelsInTheContext(t *testing.T) {
	t.Parallel()

	// A context from outside the sandbox carries no execution, which is what
	// tells a top-level call from a re-entrant one.
	_, ok := exec.FromContext(context.Background())
	assert.False(t, ok)

	want := exec.Root(1).Child(2)
	got, ok := exec.FromContext(exec.NewContext(context.Background(), want))
	assert.True(t, ok)
	assert.Equal(t, want, got)
}
