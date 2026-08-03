package builtins

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/afero"
)

// patchCommand applies a unified diff to files in the virtual filesystem. The
// patch is read from -i patchfile or stdin.
//
//	patch [-pN] [-i patchfile]
//
// /dev/null on the old side creates a file; on the new side it deletes one.
//
// Header paths are untrusted: after -pN stripping, an absolute path or one
// climbing out of the working directory is rejected.
//
// A diff is applied as a whole. Path validation, reading the originals and
// applying the hunks all happen before the first write, so a diff rejected for
// any of those reasons leaves no partial edit behind and reports nothing as
// patched. What the write phase cannot promise is the filesystem: a write that
// fails on its own, leaves the files written before it in place.
func patchCommand(_ context.Context, env *Env) error {
	fs := NewFlagSet()
	patchFileFlag := fs.String("", "-i")
	stripFlag := fs.String("0", "-p")
	operands, err := fs.Parse(env.Args)
	if err != nil {
		return err
	}
	if len(operands) > 0 {
		return fmt.Errorf("unexpected argument: %q", operands[0])
	}
	strip, err := strconv.Atoi(*stripFlag)
	if err != nil {
		return fmt.Errorf("invalid strip count: %q", *stripFlag)
	}
	data, err := readSource(env, *patchFileFlag)
	if err != nil {
		return err
	}

	patches, err := parseUnifiedDiff(string(data))
	if err != nil {
		return err
	}
	if len(patches) == 0 {
		return fmt.Errorf("no unified diff found in input")
	}

	actions, err := planPatches(env, patches, strip)
	if err != nil {
		return err
	}

	for _, a := range actions {
		if err := a.apply(env); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "patching file %s\n", a.target.display)
	}
	return nil
}

// patchAction is one section of a diff, decided but not yet carried out.
type patchAction struct {
	target patchTarget
	remove bool
	lines  []string // the content to write when remove is false
}

func (a patchAction) apply(env *Env) error {
	if a.remove {
		return env.FS.Remove(a.target.abs)
	}
	return writeLines(env, a.target.abs, a.lines)
}

// plannedFile is what a diff has decided about a path so far: gone reports a
// deletion, lines the content the file would hold.
type plannedFile struct {
	gone  bool
	lines []string
}

// planPatches turns each section of a diff into the action that carries it out,
// without touching the filesystem. Every reason a section can be rejected is
// reached here, which is what lets the caller treat the diff as a whole.
//
// A section reads the state an earlier section decided rather than the file on
// disk, so a diff naming the same path twice applies its second section to the
// result of the first, and a section naming a path an earlier one deleted is
// rejected instead of resurrecting the old content.
func planPatches(env *Env, patches []filePatch, strip int) ([]patchAction, error) {
	actions := make([]patchAction, 0, len(patches))
	planned := make(map[string]plannedFile, len(patches))

	for _, fp := range patches {
		target, err := resolvePatchTarget(env, fp.targetName(), strip)
		if err != nil {
			return nil, err
		}

		if fp.newName == "/dev/null" {
			// A deletion has no hunk to apply, so the file's presence is the only
			// thing that can reject it. Asking here rather than at Remove keeps a
			// missing file from rejecting the diff after earlier files are written.
			if err := checkDeletable(env, planned, target); err != nil {
				return nil, err
			}
			planned[target.abs] = plannedFile{gone: true}
			actions = append(actions, patchAction{target: target, remove: true})
			continue
		}

		// A creation applies its hunks to nothing, so whatever the path holds is
		// replaced rather than read.
		var orig []string
		if fp.oldName != "/dev/null" {
			orig, err = plannedLines(env, planned, target)
			if err != nil {
				return nil, err
			}
		}
		result, err := applyHunks(orig, fp.hunks)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", target.display, err)
		}
		planned[target.abs] = plannedFile{lines: result}
		actions = append(actions, patchAction{target: target, lines: result})
	}
	return actions, nil
}

// plannedLines returns the content a section should be applied to: what an
// earlier section decided, or the file as it is on disk.
func plannedLines(env *Env, planned map[string]plannedFile, target patchTarget) ([]string, error) {
	if p, ok := planned[target.abs]; ok {
		if p.gone {
			return nil, fmt.Errorf("%s: deleted earlier in this patch", target.display)
		}
		return p.lines, nil
	}
	b, err := afero.ReadFile(env.FS, target.abs)
	if err != nil {
		return nil, err
	}
	return splitLines(b), nil
}

// checkDeletable rejects a deletion whose target is already gone: a path an
// earlier section deleted, or a file absent from the filesystem. The content is
// never read.
func checkDeletable(env *Env, planned map[string]plannedFile, target patchTarget) error {
	if p, ok := planned[target.abs]; ok {
		if p.gone {
			return fmt.Errorf("%s: deleted earlier in this patch", target.display)
		}
		return nil
	}
	_, err := env.FS.Stat(target.abs)
	return err
}

// patchTarget is the file a filePatch operates on: display is the name reported
// to the user, abs the path actually read, written, or removed.
type patchTarget struct {
	display string
	abs     string
}

// resolvePatchTarget drops strip leading components from a diff header path and
// resolves the rest against the working directory.
//
// A path that is still absolute after stripping is rejected instead of being
// read as relative to the working directory, patch edits a file the diff names,
// and re-pointing that name at another location would substitute the file being edited.
// A path climbing out with ".." is rejected for the same reason.
// GNU patch has refused both since the fix for CVE-2010-4651.
func resolvePatchTarget(env *Env, name string, strip int) (patchTarget, error) {
	display := stripPath(name, strip)
	if path.IsAbs(display) {
		return patchTarget{}, fmt.Errorf("%q is an absolute path", display)
	}
	abs, err := containedPath(env.Dir, display)
	if err != nil {
		return patchTarget{}, err
	}
	return patchTarget{display: display, abs: abs}, nil
}

func writeLines(env *Env, abs string, lines []string) error {
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	return afero.WriteFile(env.FS, abs, []byte(content), 0o644)
}

// stripPath drops n leading path components, as patch's -pN does.
func stripPath(p string, n int) string {
	if p == "/dev/null" {
		return p
	}
	parts := strings.Split(p, "/")
	if n >= len(parts) {
		return parts[len(parts)-1]
	}
	return strings.Join(parts[n:], "/")
}

type patchLine struct {
	kind byte // ' ', '-', or '+'
	text string
}

type patchHunk struct {
	oldStart int
	lines    []patchLine
}

type filePatch struct {
	oldName string
	newName string
	hunks   []patchHunk
}

// targetName is the header path the section operates on: the new name, or the
// old one when the section deletes the file.
func (fp filePatch) targetName() string {
	if fp.newName == "/dev/null" {
		return fp.oldName
	}
	return fp.newName
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parseUnifiedDiff(data string) ([]filePatch, error) {
	lines := strings.Split(data, "\n")
	var patches []filePatch
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "--- ") {
			i++
			continue
		}
		oldName := parseDiffName(lines[i][4:])
		i++
		if i >= len(lines) || !strings.HasPrefix(lines[i], "+++ ") {
			return nil, fmt.Errorf("malformed header: expected +++ after ---")
		}
		newName := parseDiffName(lines[i][4:])
		i++

		fp := filePatch{oldName: oldName, newName: newName}
		for i < len(lines) && strings.HasPrefix(lines[i], "@@") {
			h, next, err := parseHunk(lines, i)
			if err != nil {
				return nil, err
			}
			fp.hunks = append(fp.hunks, h)
			i = next
		}
		patches = append(patches, fp)
	}
	return patches, nil
}

// parseDiffName extracts the path from a header line, dropping the trailing
// tab-separated timestamp that many diff tools append.
func parseDiffName(s string) string {
	if idx := strings.IndexByte(s, '\t'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func parseHunk(lines []string, i int) (patchHunk, int, error) {
	m := hunkHeader.FindStringSubmatch(lines[i])
	if m == nil {
		return patchHunk{}, i, fmt.Errorf("malformed hunk header: %q", lines[i])
	}
	oldStart, _ := strconv.Atoi(m[1])
	oldCount := 1
	if m[2] != "" {
		oldCount, _ = strconv.Atoi(m[2])
	}
	newCount := 1
	if m[4] != "" {
		newCount, _ = strconv.Atoi(m[4])
	}
	i++

	h := patchHunk{oldStart: oldStart}
	oldRemain, newRemain := oldCount, newCount
	for i < len(lines) && (oldRemain > 0 || newRemain > 0) {
		// An empty line is a context line whose leading space was stripped.
		kind, text := byte(' '), ""
		if l := lines[i]; l != "" {
			kind, text = l[0], l[1:]
		}
		switch kind {
		case ' ':
			h.lines = append(h.lines, patchLine{kind, text})
			oldRemain--
			newRemain--
		case '-':
			h.lines = append(h.lines, patchLine{kind, text})
			oldRemain--
		case '+':
			h.lines = append(h.lines, patchLine{kind, text})
			newRemain--
		case '\\':
			// "\ No newline at end of file" — ignore.
		default:
			return patchHunk{}, i, fmt.Errorf("unexpected hunk line: %q", lines[i])
		}
		i++
	}
	if oldRemain > 0 || newRemain > 0 {
		return patchHunk{}, i, fmt.Errorf("hunk ended before its line counts were met")
	}
	return h, i, nil
}

func applyHunks(orig []string, hunks []patchHunk) ([]string, error) {
	var out []string
	srcIdx := 0
	for _, h := range hunks {
		start := h.oldStart - 1
		if start < srcIdx {
			start = srcIdx
		}
		if start > len(orig) {
			return nil, fmt.Errorf("hunk starts beyond end of file")
		}
		out = append(out, orig[srcIdx:start]...)
		srcIdx = start

		for _, hl := range h.lines {
			switch hl.kind {
			case ' ':
				if srcIdx >= len(orig) || orig[srcIdx] != hl.text {
					return nil, fmt.Errorf("context mismatch at line %d", srcIdx+1)
				}
				out = append(out, orig[srcIdx])
				srcIdx++
			case '-':
				if srcIdx >= len(orig) || orig[srcIdx] != hl.text {
					return nil, fmt.Errorf("deletion mismatch at line %d", srcIdx+1)
				}
				srcIdx++
			case '+':
				out = append(out, hl.text)
			}
		}
	}
	out = append(out, orig[srcIdx:]...)
	return out, nil
}

func init() {
	Register("patch", patchCommand)
}
