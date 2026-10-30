package builtins

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/afero"

	"github.com/mrtc0/sbsh/sandbox/command"
)

// Exit codes zip shares with Info-ZIP. Fatal errors exit 1, as every other
// builtin's do.
const (
	// zipExitNothingToDo is Info-ZIP's 12: no operand produced an entry.
	zipExitNothingToDo = 12
	// zipExitPartial is Info-ZIP's 18: an operand could not be read, and the
	// archive was written without it.
	zipExitPartial = 18

	// unzipExitWarning is Info-ZIP's 1: some members were skipped.
	unzipExitWarning = 1
	// unzipExitNoMatch is Info-ZIP's 11: a member operand matched nothing.
	unzipExitNoMatch = 11
)

// zipCommand creates a zip archive, or updates an existing one: an entry whose
// name is already in the archive is replaced in place, and the rest are
// appended. Entry names are relative to the working directory.
//
//	-r recurse into directories / -j store file names only (junk paths)
//	-q quiet
//	zip [-rjq] archive[.zip] file...
//	zip [-rjq] - file...       (write the archive to stdout)
func zipCommand(_ context.Context, inv *command.Invocation) error {
	flags := NewFlagSet()
	recurse := flags.Bool("-r", "--recurse-paths")
	junk := flags.Bool("-j", "--junk-paths")
	quiet := flags.Bool("-q", "--quiet")
	args, err := flags.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	if len(args) < 2 {
		return command.Exit(1, "usage: zip [-rjq] archive file...")
	}
	archive, files := args[0], args[1:]

	b := &zipBuilder{
		inv:    inv,
		guard:  &walkGuard{inv: inv},
		junk:   *junk,
		byName: map[string]*zipEntry{},
	}
	// Progress goes to stderr when the archive itself is on stdout.
	progress := inv.Stdout
	toStdout := archive == "-"
	if toStdout {
		progress = inv.Stderr
	} else {
		// Info-ZIP's rule: a name without an extension gets ".zip".
		if path.Ext(archive) == "" {
			archive += ".zip"
		}
		b.archive = inv.Abs(archive)
	}
	if *quiet {
		progress = io.Discard
	}

	for _, f := range files {
		if err := b.add(f, *recurse); err != nil {
			return command.Exitf(1, "%v", err)
		}
	}

	var old *zip.Reader
	if !toStdout {
		old, err = openExistingZip(inv, b.archive)
		if err != nil {
			return command.Exitf(1, "%s: %v", archive, err)
		}
	}
	if len(b.entries) == 0 && old == nil {
		return command.Exit(zipExitNothingToDo, "nothing to do")
	}

	var buf bytes.Buffer
	if err := b.write(&buf, old, progress); err != nil {
		return command.Exitf(1, "%v", err)
	}
	// As with tar, the archive is written even when an operand was refused: it
	// holds everything that could be read, and the exit code says it is not
	// everything that was asked for.
	if toStdout {
		if _, err := inv.Stdout.Write(buf.Bytes()); err != nil {
			return command.Exitf(1, "%v", err)
		}
	} else if err := afero.WriteFile(inv.FS, b.archive, buf.Bytes(), 0o644); err != nil {
		return command.Exitf(1, "%v", err)
	}
	if b.guard.refused {
		return command.Exit(zipExitPartial)
	}
	return nil
}

// zipEntry is one entry gathered for the archive, already compressed.
type zipEntry struct {
	src  string // VFS path the entry was read from
	hdr  *zip.FileHeader
	data []byte
}

// zipBuilder gathers entries before anything is written, so that an update can
// put each one where the entry it replaces stood.
type zipBuilder struct {
	inv     *command.Invocation
	guard   *walkGuard
	junk    bool
	archive string // VFS path of the archive being written, or "" for stdout

	entries []*zipEntry
	byName  map[string]*zipEntry
}

// add gathers the operand f: the file itself, or a directory as an entry of its
// own and, with recurse, everything below it.
func (b *zipBuilder) add(f string, recurse bool) error {
	root := b.inv.Abs(f)
	if _, _, err := b.inv.FS.LstatIfPossible(root); errors.Is(err, fs.ErrNotExist) {
		// Info-ZIP warns and carries on; the exit status says so here as well,
		// so a misspelled operand does not pass for a complete archive.
		b.guard.report(fmt.Errorf("name not matched: %s", f))
		return nil
	}
	return afero.Walk(b.inv.FS, root, b.guard.wrap(func(p string, info os.FileInfo, _ error) error {
		// A link is reported and left out, for the reason tar gives: unzip has
		// no way to recreate it, and the target's bytes under the link's name
		// would put a file in the archive that the tree does not have.
		if info.Mode()&os.ModeSymlink != 0 {
			b.guard.report(fmt.Errorf("%s: symbolic link not archived", p))
			return nil
		}
		// The archive being written is never an entry of itself.
		if p == b.archive {
			return nil
		}
		if err := b.addEntry(f, root, p, info); err != nil {
			return err
		}
		if info.IsDir() && !recurse {
			return filepath.SkipDir
		}
		return nil
	}))
}

func (b *zipBuilder) addEntry(arg, root, p string, info os.FileInfo) error {
	name := zipEntryName(walkedName(arg, root, p))
	if b.junk {
		if info.IsDir() {
			// -j stores files only; a directory contributes its contents.
			return nil
		}
		name = path.Base(name)
	}
	if name == "" {
		return nil
	}
	if info.IsDir() {
		name += "/"
	}
	if prev, ok := b.byName[name]; ok {
		if prev.src == p {
			return nil // the same file named twice
		}
		return fmt.Errorf("%s: cannot repeat names in zip file (from %s and %s)", name, prev.src, p)
	}

	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = name
	// CreateRaw, unlike CreateHeader, leaves the flags as given, so the UTF-8
	// bit is set here. Without it a reader takes a non-ASCII name as CP437.
	if !isASCII(name) && utf8.ValidString(name) {
		hdr.Flags |= zipFlagUTF8
	}
	e := &zipEntry{src: p, hdr: hdr}
	if !info.IsDir() {
		raw, err := afero.ReadFile(b.inv.FS, p)
		if err != nil {
			return err
		}
		if err := e.compress(raw); err != nil {
			return err
		}
	} else {
		// FileInfoHeader takes a directory's size from stat, which is the
		// size of its listing and not of any contents the entry holds.
		hdr.UncompressedSize64 = 0
		hdr.Method = zip.Store
	}
	b.entries = append(b.entries, e)
	b.byName[name] = e
	return nil
}

// zipFlagUTF8 is general purpose bit 11: the name is UTF-8 rather than CP437.
const zipFlagUTF8 = 0x800

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// zipEntryName turns a walked display name into an entry name. Names are always
// relative and never climb: a leading "/" and leading ".." components are
// dropped, which is what Info-ZIP does with "zip a.zip ../x" or "/etc/hosts". An
// operand naming the working directory itself yields "".
func zipEntryName(name string) string {
	name = path.Clean(name)
	for {
		switch {
		case strings.HasPrefix(name, "/"):
			name = name[1:]
		case name == "..":
			name = ""
		case strings.HasPrefix(name, "../"):
			name = name[3:]
		default:
			if name == "." {
				return ""
			}
			return name
		}
	}
}

// compress deflates raw, or stores it when deflating does not make it smaller —
// the choice Info-ZIP makes, and the reason the work is done here rather than
// left to zip.Writer, which always deflates.
func (e *zipEntry) compress(raw []byte) error {
	e.hdr.CRC32 = crc32.ChecksumIEEE(raw)
	e.hdr.UncompressedSize64 = uint64(len(raw))

	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return err
	}
	if _, err := fw.Write(raw); err != nil {
		return err
	}
	if err := fw.Close(); err != nil {
		return err
	}
	if buf.Len() < len(raw) {
		e.hdr.Method = zip.Deflate
		e.data = buf.Bytes()
	} else {
		e.hdr.Method = zip.Store
		e.data = raw
	}
	e.hdr.CompressedSize64 = uint64(len(e.data))
	return nil
}

// write produces the archive: the old entries in their order, each replaced by
// a gathered entry of the same name, followed by the gathered entries that are
// new.
func (b *zipBuilder) write(w io.Writer, old *zip.Reader, progress io.Writer) error {
	zw := zip.NewWriter(w)
	written := map[string]bool{}
	if old != nil {
		for _, f := range old.File {
			e, ok := b.byName[f.Name]
			if !ok {
				if err := zw.Copy(f); err != nil {
					return err
				}
				continue
			}
			if err := b.writeEntry(zw, e, "updating", progress); err != nil {
				return err
			}
			written[f.Name] = true
		}
	}
	for _, e := range b.entries {
		if written[e.hdr.Name] {
			continue
		}
		if err := b.writeEntry(zw, e, "adding", progress); err != nil {
			return err
		}
	}
	return zw.Close()
}

func (b *zipBuilder) writeEntry(zw *zip.Writer, e *zipEntry, verb string, progress io.Writer) error {
	w, err := zw.CreateRaw(e.hdr)
	if err != nil {
		return err
	}
	if _, err := w.Write(e.data); err != nil {
		return err
	}
	ratio := 0
	if e.hdr.UncompressedSize64 > 0 {
		ratio = int(100 - e.hdr.CompressedSize64*100/e.hdr.UncompressedSize64)
	}
	method := "stored"
	if e.hdr.Method == zip.Deflate {
		method = "deflated"
	}
	fmt.Fprintf(progress, "  %s: %s (%s %d%%)\n", verb, e.hdr.Name, method, ratio)
	return nil
}

// openExistingZip returns the archive at p for updating, or nil when there is
// none yet. Anything there that is not a zip archive is an error rather than
// something to write over.
func openExistingZip(inv *command.Invocation, p string) (*zip.Reader, error) {
	raw, err := afero.ReadFile(inv.FS, p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return newZipReader(raw)
}

// newZipReader opens an in-memory archive. archive/zip may flag a non-local
// name with ErrInsecurePath alongside a usable reader; names are confined by
// the commands themselves, so that is not a reason to refuse the archive.
func newZipReader(raw []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, err
	}
	return zr, nil
}

// unzipCommand lists, extracts, or prints the members of a zip archive. Member
// operands select members by exact name or by a path.Match pattern.
//
//	-l list members / -p write member contents to stdout
//	-d DIR extract into DIR (created if missing)
//	-o overwrite existing files / -n never overwrite existing files
//	-q quiet
//	unzip [-qo|-qn] [-d dir] archive[.zip] [member...]
//	unzip -l archive[.zip] [member...]
//	unzip -p archive[.zip] [member...]
func unzipCommand(_ context.Context, inv *command.Invocation) error {
	flags := NewFlagSet()
	list := flags.Bool("-l")
	pipe := flags.Bool("-p")
	overwrite := flags.Bool("-o")
	never := flags.Bool("-n")
	quiet := flags.Bool("-q")
	destFlag := flags.String("", "-d")
	args, err := flags.Parse(inv.Args)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	if len(args) == 0 {
		return command.Exit(1, "usage: unzip [-lpqon] [-d dir] archive [member...]")
	}
	if *list && *pipe {
		return command.Exit(1, "conflicting modes -- l and p")
	}
	if *overwrite && *never {
		return command.Exit(1, "conflicting options -- o and n")
	}

	archive, patterns := args[0], args[1:]
	raw, archive, err := readZipArchive(inv, archive)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	zr, err := newZipReader(raw)
	if err != nil {
		return command.Exitf(1, "%s: %v", archive, err)
	}

	members, unmatched, err := selectZipMembers(zr.File, patterns)
	if err != nil {
		return command.Exitf(1, "%v", err)
	}
	for _, p := range unmatched {
		fmt.Fprintf(inv.Stderr, "%s: filename not matched: %s\n", inv.Name, p)
	}

	switch {
	case *list:
		unzipList(inv, archive, members)
	case *pipe:
		if err := unzipPipe(inv, members); err != nil {
			return command.Exitf(1, "%v", err)
		}
	default:
		progress := inv.Stdout
		if *quiet {
			progress = io.Discard
		}
		dest := inv.Dir
		if *destFlag != "" {
			dest = inv.Abs(*destFlag)
		}
		policy := unzipAsk
		if *overwrite {
			policy = unzipOverwrite
		} else if *never {
			policy = unzipKeep
		}
		fmt.Fprintf(progress, "Archive:  %s\n", archive)
		skipped, err := unzipExtract(inv, members, dest, policy, progress)
		if err != nil {
			return command.Exitf(1, "%v", err)
		}
		if skipped && len(unmatched) == 0 {
			return command.Exit(unzipExitWarning)
		}
	}
	if len(unmatched) > 0 {
		return command.Exit(unzipExitNoMatch)
	}
	return nil
}

// readZipArchive reads the archive operand, trying "<name>.zip" when name itself
// does not exist, as Info-ZIP does. It returns the name that was read.
func readZipArchive(inv *command.Invocation, name string) ([]byte, string, error) {
	raw, err := afero.ReadFile(inv.FS, inv.Abs(name))
	if errors.Is(err, fs.ErrNotExist) && !strings.HasSuffix(name, ".zip") {
		if alt, altErr := afero.ReadFile(inv.FS, inv.Abs(name+".zip")); altErr == nil {
			return alt, name + ".zip", nil
		}
	}
	return raw, name, err
}

// selectZipMembers returns the members matching any of patterns, in archive
// order, and the patterns that matched nothing. No patterns selects everything.
func selectZipMembers(files []*zip.File, patterns []string) ([]*zip.File, []string, error) {
	if len(patterns) == 0 {
		return files, nil, nil
	}
	matched := make([]bool, len(patterns))
	var out []*zip.File
	for _, f := range files {
		hit := false
		for i, p := range patterns {
			ok, err := path.Match(p, f.Name)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %v", p, err)
			}
			if ok || p == f.Name {
				matched[i] = true
				hit = true
			}
		}
		if hit {
			out = append(out, f)
		}
	}
	var unmatched []string
	for i, p := range patterns {
		if !matched[i] {
			unmatched = append(unmatched, p)
		}
	}
	return out, unmatched, nil
}

// unzipList prints members in the layout of Info-ZIP's "unzip -l", with the
// ISO dates the Debian and Ubuntu builds print.
func unzipList(inv *command.Invocation, archive string, members []*zip.File) {
	w := inv.Stdout
	fmt.Fprintf(w, "Archive:  %s\n", archive)
	fmt.Fprintln(w, "  Length      Date    Time    Name")
	fmt.Fprintln(w, "---------  ---------- -----   ----")
	var total uint64
	for _, f := range members {
		total += f.UncompressedSize64
		fmt.Fprintf(w, "%9d  %s   %s\n", f.UncompressedSize64, f.Modified.Format("2006-01-02 15:04"), f.Name)
	}
	fmt.Fprintln(w, "---------                     -------")
	noun := "files"
	if len(members) == 1 {
		noun = "file"
	}
	fmt.Fprintf(w, "%9d                     %d %s\n", total, len(members), noun)
}

func unzipPipe(inv *command.Invocation, members []*zip.File) error {
	for _, f := range members {
		if f.FileInfo().IsDir() {
			continue
		}
		if err := checkZipMember(f); err != nil {
			return err
		}
		if err := copyZipMember(inv.Stdout, f); err != nil {
			return err
		}
	}
	return nil
}

// unzipPolicy is what extraction does with a file that is already there. There
// is no terminal to ask on, so asking means refusing: the extraction stops
// before writing anything and says which option would let it through.
type unzipPolicy int

const (
	unzipAsk unzipPolicy = iota
	unzipOverwrite
	unzipKeep
)

// unzipTarget is one member and where it lands.
type unzipTarget struct {
	f      *zip.File
	target string
}

// unzipExtract writes members under dest and reports whether any member was
// skipped with a warning.
//
// Member names are untrusted. Unlike a tar stream, a zip archive lists every
// member up front, so the whole archive is checked before the first write: a
// member climbing out of dest with "..", one stored with a method or encryption
// this unzip cannot read, or a file that is already there (without -o or -n)
// fails the extraction with nothing written. A leading "/" is dropped, the way
// tar and Info-ZIP do.
func unzipExtract(inv *command.Invocation, members []*zip.File, dest string, policy unzipPolicy, progress io.Writer) (bool, error) {
	var plan, dirs []unzipTarget
	// planned is keyed by the file a write lands on, not by its name: under an
	// existing link alias -> real, "alias/x" and "real/x" are one file, and the
	// second member to reach it conflicts with the first.
	planned := map[string]bool{}
	skipped := false
	for _, f := range members {
		target, err := containedPath(dest, f.Name)
		if err != nil {
			return false, err
		}
		if target == dest {
			continue // "", "." or "/" name the destination itself
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			// The virtual filesystem cannot create a link, and writing the
			// target's name as file contents would leave a file the archive
			// does not describe.
			fmt.Fprintf(inv.Stderr, "%s: %s: symbolic link not extracted\n", inv.Name, f.Name)
			skipped = true
			continue
		}
		if mode.IsDir() {
			plan = append(plan, unzipTarget{f, target})
			// A directory that is already there keeps its mode; one this
			// extraction creates gets the archive's, once its contents are in.
			if _, err := inv.FS.Stat(target); errors.Is(err, fs.ErrNotExist) && zipHasUnixMode(f) && mode.Perm() != 0 {
				dirs = append(dirs, unzipTarget{f, target})
			}
			continue
		}
		if err := checkZipMember(f); err != nil {
			return false, err
		}
		key := target
		// A resolution error is left for the write to report: that write
		// would fail the same way.
		if real, err := inv.FS.EvalSymlinks(target); err == nil {
			key = real
		}
		if _, err := inv.FS.Stat(target); err == nil || planned[key] {
			switch policy {
			case unzipAsk:
				return false, fmt.Errorf("%s: already exists; use -o to overwrite or -n to keep it", f.Name)
			case unzipKeep:
				continue
			}
		}
		planned[key] = true
		plan = append(plan, unzipTarget{f, target})
	}

	for _, t := range plan {
		if err := extractZipMember(inv, t, progress); err != nil {
			return skipped, err
		}
	}
	// Directories are created writable so their contents can go in, and get
	// their own mode afterwards, deepest first: a parent without write or
	// search permission would otherwise stop the chmod of what is below it.
	sort.SliceStable(dirs, func(i, j int) bool {
		return strings.Count(dirs[i].target, "/") > strings.Count(dirs[j].target, "/")
	})
	for _, d := range dirs {
		if err := inv.FS.Chmod(d.target, d.f.Mode().Perm()); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// zipHasUnixMode reports whether a member records Unix permission bits. An
// archive made on DOS or Windows records only a read-only flag, which
// archive/zip reads as 0777 for a directory; that is not a mode to apply.
func zipHasUnixMode(f *zip.File) bool {
	const creatorUnix, creatorMacOSX = 3, 19
	switch f.CreatorVersion >> 8 {
	case creatorUnix, creatorMacOSX:
		return true
	}
	return false
}

// checkZipMember fails for a member whose contents this unzip cannot read,
// before anything is written for it.
func checkZipMember(f *zip.File) error {
	if f.Flags&0x1 != 0 {
		return fmt.Errorf("%s: encrypted members are not supported", f.Name)
	}
	if f.Method != zip.Store && f.Method != zip.Deflate {
		return fmt.Errorf("%s: unsupported compression method %d", f.Name, f.Method)
	}
	return nil
}

func extractZipMember(inv *command.Invocation, t unzipTarget, progress io.Writer) error {
	if t.f.Mode().IsDir() {
		fmt.Fprintf(progress, "   creating: %s\n", t.f.Name)
		return inv.FS.MkdirAll(t.target, 0o755)
	}
	if err := inv.FS.MkdirAll(path.Dir(t.target), 0o755); err != nil {
		return err
	}
	fmt.Fprintf(progress, "  inflating: %s\n", t.f.Name)
	// Only permission bits are carried over: setuid, setgid and sticky from an
	// archive are not something to apply to a file in a mounted directory.
	perm := t.f.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := inv.FS.OpenFile(t.target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if err := copyZipMember(out, t.f); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return inv.FS.Chtimes(t.target, t.f.Modified, t.f.Modified)
}

// copyZipMember streams a member's contents to w. archive/zip checks the size
// and CRC-32 against the header as it reads, so a member that lies about either
// fails here.
func copyZipMember(w io.Writer, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%s: %v", f.Name, err)
	}
	defer rc.Close()
	if _, err := io.Copy(w, rc); err != nil {
		return fmt.Errorf("%s: %v", f.Name, err)
	}
	return nil
}

func init() {
	Register("zip", zipCommand)
	Register("unzip", unzipCommand)
}
