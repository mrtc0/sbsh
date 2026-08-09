package command

import (
	"context"
	"testing"
)

func TestUnresolvedRecorder(t *testing.T) {
	t.Parallel()

	ctx, unresolved := WithUnresolvedRecorder(context.Background())

	if name, ok := unresolved(); ok {
		t.Errorf("unresolved() = %q, %v before anything was recorded; want unset", name, ok)
	}

	RecordUnresolved(ctx, "first")
	RecordUnresolved(ctx, "second")

	name, ok := unresolved()
	if !ok {
		t.Fatal("unresolved() reported nothing after a name was recorded")
	}
	// The first name is the one that explains the failure; the rest are what a
	// script kept doing afterwards.
	if name != "first" {
		t.Errorf("unresolved() = %q, want %q", name, "first")
	}
}

// TestRecordUnresolvedWithoutRecorder covers a command dispatched outside any
// execution that installed a recorder: there is nowhere to record, and that is
// not a failure.
func TestRecordUnresolvedWithoutRecorder(t *testing.T) {
	t.Parallel()
	RecordUnresolved(context.Background(), "nosuchcommand")
}

// TestUnresolvedRecordersDoNotNest checks that a child's unresolved name stays
// with the child, so a parent is not blamed for what its child could not find.
func TestUnresolvedRecordersDoNotNest(t *testing.T) {
	t.Parallel()

	parentCtx, parentUnresolved := WithUnresolvedRecorder(context.Background())
	childCtx, childUnresolved := WithUnresolvedRecorder(parentCtx)

	RecordUnresolved(childCtx, "childonly")

	if name, ok := childUnresolved(); !ok || name != "childonly" {
		t.Errorf("child unresolved() = %q, %v; want %q, true", name, ok, "childonly")
	}
	if name, ok := parentUnresolved(); ok {
		t.Errorf("parent unresolved() = %q, %v; want unset", name, ok)
	}
}
