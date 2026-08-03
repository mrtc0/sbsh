package builtins

import (
	"context"
	"fmt"
	"strings"

	"github.com/mrtc0/sbsh/sandbox/command"
)

const diffContext = 3

// diffCommand compares two files and prints a unified diff. It exits 0 when the
// files are identical and 1 when they differ, matching diff's convention so the
// output feeds straight into patch.
//
//	diff [-u] file1 file2
//
// Only the unified format is produced; -u is accepted for compatibility.
func diffCommand(_ context.Context, inv *command.Invocation) error {
	fs := NewFlagSet()
	fs.Bool("-u") // unified format is the only output; accepted for compatibility
	files, err := fs.Parse(inv.Args)
	if err != nil {
		return err
	}
	if len(files) != 2 {
		return fmt.Errorf("usage: diff [-u] file1 file2")
	}
	a, err := readSource(inv, files[0])
	if err != nil {
		return err
	}
	b, err := readSource(inv, files[1])
	if err != nil {
		return err
	}

	hunks := unifiedHunks(splitLines(a), splitLines(b))
	if len(hunks) == 0 {
		return nil
	}
	fmt.Fprintf(inv.Stdout, "--- %s\n", files[0])
	fmt.Fprintf(inv.Stdout, "+++ %s\n", files[1])
	for _, h := range hunks {
		fmt.Fprint(inv.Stdout, h)
	}
	return exit(1)
}

type diffOp struct {
	kind byte // ' ', '-', or '+'
	text string
}

// diffLines produces an in-order edit script using a longest-common-subsequence
// table. Adequate for the file sizes an agent edits; not tuned for huge inputs.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// unifiedHunks groups the edit script into unified-diff hunks with context
// lines. Change regions closer than 2*context merge into one hunk.
func unifiedHunks(a, b []string) []string {
	ops := diffLines(a, b)

	var changes []int
	for idx, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, idx)
		}
	}
	if len(changes) == 0 {
		return nil
	}

	var groups [][2]int
	gs, ge := changes[0], changes[0]
	for _, c := range changes[1:] {
		if c-ge-1 <= 2*diffContext {
			ge = c
		} else {
			groups = append(groups, [2]int{gs, ge})
			gs, ge = c, c
		}
	}
	groups = append(groups, [2]int{gs, ge})

	var hunks []string
	for _, g := range groups {
		start := g[0] - diffContext
		if start < 0 {
			start = 0
		}
		end := g[1] + diffContext + 1
		if end > len(ops) {
			end = len(ops)
		}
		hunks = append(hunks, formatHunk(ops, start, end))
	}
	return hunks
}

func formatHunk(ops []diffOp, start, end int) string {
	oldStart, newStart := 1, 1
	for k := 0; k < start; k++ {
		switch ops[k].kind {
		case ' ':
			oldStart++
			newStart++
		case '-':
			oldStart++
		case '+':
			newStart++
		}
	}

	var oldCount, newCount int
	var body strings.Builder
	for k := start; k < end; k++ {
		op := ops[k]
		switch op.kind {
		case ' ':
			oldCount++
			newCount++
		case '-':
			oldCount++
		case '+':
			newCount++
		}
		body.WriteByte(op.kind)
		body.WriteString(op.text)
		body.WriteByte('\n')
	}

	return fmt.Sprintf("@@ -%d,%d +%d,%d @@\n%s", oldStart, oldCount, newStart, newCount, body.String())
}

func init() {
	Register("diff", diffCommand)
}
