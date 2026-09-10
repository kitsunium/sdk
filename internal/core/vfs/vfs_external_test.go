package vfs_test

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// fourMethodDouble is a WritableFS that implements the port and NOTHING else.
// It is the ADR 0039 guard in its executable form: the day someone adds a
// fifth method to WritableFS, this type stops satisfying the interface and the
// package stops compiling — which is precisely what would happen to every
// downstream implementation, except that theirs would break after release.
type fourMethodDouble struct{}

// Open satisfies the embedded fs.FS half.
func (fourMethodDouble) Open(_ string) (fs.File, error) { return nil, fs.ErrNotExist }

// WriteFile satisfies the first write verb.
func (fourMethodDouble) WriteFile(_ string, _ []byte, _ fs.FileMode) error { return nil }

// MkdirAll satisfies the second.
func (fourMethodDouble) MkdirAll(_ string, _ fs.FileMode) error { return nil }

// Remove satisfies the third.
func (fourMethodDouble) Remove(_ string) error { return nil }

// RemoveAll satisfies the fourth.
func (fourMethodDouble) RemoveAll(_ string) error { return nil }

// TestWritableFSStaysFrozenAtFiveMethods pins the port's width: the four write
// verbs plus the one Open inherited from the embedded fs.FS.
//
// The count is asserted as a NUMBER as well as by the double above, because
// the two failures differ. A new method breaks the double at compile time; a
// method REMOVED, or an embedded interface swapped for a narrower one, keeps
// the double compiling and silently shrinks what consumers may rely on.
func TestWritableFSStaysFrozenAtFiveMethods(t *testing.T) {
	t.Parallel()
	//: the assignment is the assertion: a type carrying exactly these five
	//: methods and no others still satisfies the port.
	port := corevfs.WritableFS(fourMethodDouble{})
	if _, openErr := port.Open("x"); openErr == nil {
		t.Fatal("the double's Open answered — the wrong method was bound")
	}
	const want int = 5
	if got := reflect.TypeFor[corevfs.WritableFS]().NumMethod(); got != want {
		t.Errorf("WritableFS has %d methods, want %d — see ADR 0039 before changing it", got, want)
	}
}

// TestAtomicWriterIsASiblingAndNotAMember checks the shape ADR 0039 prescribes:
// the capability is REACHED BY ASSERTION, so a filesystem that cannot publish
// atomically is still a perfectly legal WritableFS. If WriteAtomic ever
// migrates into the port, every such implementation stops compiling.
func TestAtomicWriterIsASiblingAndNotAMember(t *testing.T) {
	t.Parallel()
	writable := reflect.TypeFor[corevfs.WritableFS]()
	if _, found := writable.MethodByName("WriteAtomic"); found {
		t.Error("WriteAtomic is a method of WritableFS; it must stay a sibling interface")
	}
	//: and the four-method double must NOT be mistaken for a publisher.
	port := corevfs.WritableFS(fourMethodDouble{})
	if _, ok := port.(corevfs.AtomicWriter); ok {
		t.Error("a WritableFS with no WriteAtomic satisfied AtomicWriter")
	}
	const want int = 1
	if got := reflect.TypeFor[corevfs.AtomicWriter]().NumMethod(); got != want {
		t.Errorf("AtomicWriter has %d methods, want %d", got, want)
	}
}

// TestFSIsTheStdlibTypeUnchanged is the executable form of the claim that this
// SDK does not reimplement fs.WalkDir or fs.Glob. Those functions take an
// fs.FS; they work on a WritableFS only because vfs.FS is an ALIAS rather than
// a look-alike interface. A contributor who redeclares it as `type FS
// interface { Open(...) }` would keep everything compiling inside the SDK and
// break every stdlib walker at the boundary.
func TestFSIsTheStdlibTypeUnchanged(t *testing.T) {
	t.Parallel()
	if reflect.TypeFor[corevfs.FS]() != reflect.TypeFor[fs.FS]() {
		t.Fatal("vfs.FS is no longer an alias of io/fs.FS — fs.WalkDir and fs.Glob stop applying")
	}
	//: the assignment is the part that matters at a call site: a WritableFS
	//: is handed to an fs.FS parameter with no conversion whatsoever.
	port := corevfs.WritableFS(fourMethodDouble{})
	reader := fs.FS(port)
	matches, globErr := fs.Glob(reader, "*")
	if globErr != nil {
		t.Fatalf("fs.Glob over the port returned %v, want nil", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("fs.Glob matched %v on an empty double, want nothing", matches)
	}
}

// TestValidatePathRefusesEveryEscapeGrammar is the lexical half of the
// confinement promise. Every rejected case here is a real traversal attempt
// somebody has shipped: the classic relative climb, the absolute path, the
// Windows separator that a naive splitter treats as one element, and the
// embedded climb that only shows up after a join.
func TestValidatePathRefusesEveryEscapeGrammar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		path    string
		refused bool
	}{
		{"a plain name", "index.html", false},
		{"a nested name", "assets/css/site.css", false},
		{"the root reads fine", ".", false},
		{"the classic climb", "../../etc/passwd", true},
		{"a climb in the middle", "assets/../../etc/passwd", true},
		{"a bare parent element", "..", true},
		{"an absolute path", "/etc/passwd", true},
		{"a trailing slash", "assets/", true},
		{"a doubled separator", "assets//site.css", true},
		{"a current-directory element", "./site.css", true},
		{"the empty string", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := corevfs.ValidatePath(tc.path)
			if tc.refused && !kerrs.HasCode(err, corevfs.CodeInvalidPath) {
				t.Fatalf("ValidatePath(%q) = %v, want INVALID_PATH", tc.path, err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("ValidatePath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// TestABackslashIsAFilenameAndNeverASeparator records a result that surprises
// people, and that a reader of the grammar has to know: fs.ValidPath ACCEPTS
// `..\..\etc\passwd`, because the separator in this domain is "/" and nothing
// else, so that string is one legal — if peculiar — filename.
//
// It is confined, which is the property that matters: the file lands inside
// the root under that literal name, and no parent directory is reached. The
// escape a reader fears here is a Windows-shaped path meeting a Windows-shaped
// resolver, and the implementation is what refuses that, not the grammar.
//
// The test exists so nobody "fixes" the grammar by rejecting backslashes and
// silently makes a legal POSIX filename unwritable.
func TestABackslashIsAFilenameAndNeverASeparator(t *testing.T) {
	t.Parallel()
	const windowsShaped = `..\..\etc\passwd`
	if err := corevfs.ValidateWritePath(windowsShaped); err != nil {
		t.Fatalf("ValidateWritePath(%q) = %v, want nil — it is one filename here", windowsShaped, err)
	}
	if strings.Contains(windowsShaped, "/") {
		t.Fatal("the fixture stopped being separator-free; the case no longer tests what it says")
	}
}

// TestValidateWritePathRefusesTheRoot pins the one place the write grammar is
// deliberately narrower than the read grammar. `RemoveAll(".")` empties the
// entire filesystem and looks, in a diff, exactly like a no-op.
func TestValidateWritePathRefusesTheRoot(t *testing.T) {
	t.Parallel()
	if err := corevfs.ValidatePath("."); err != nil {
		t.Fatalf("ValidatePath(%q) = %v, want nil — readers legitimately list the root", ".", err)
	}
	if err := corevfs.ValidateWritePath("."); !kerrs.HasCode(err, corevfs.CodeInvalidPath) {
		t.Fatalf("ValidateWritePath(%q) = %v, want INVALID_PATH", ".", err)
	}
	if err := corevfs.ValidateWritePath("index.html"); err != nil {
		t.Fatalf("ValidateWritePath(%q) = %v, want nil", "index.html", err)
	}
}

// TestValidatePermRefusesRatherThanDefaulting is ADR 0031's refusing half made
// executable. A zero mode must never be quietly turned into 0644 — and a mode
// carrying setuid must never be applied because someone typed one digit too
// many in an octal literal.
func TestValidatePermRefusesRatherThanDefaulting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		perm    fs.FileMode
		refused bool
	}{
		{"an ordinary file mode", 0o644, false},
		{"an owner-only mode", 0o600, false},
		{"a directory mode", 0o755, false},
		{"the zero mode is never a default", 0, true},
		{"setuid", 0o755 | fs.ModeSetuid, true},
		{"setgid", 0o755 | fs.ModeSetgid, true},
		{"sticky", 0o777 | fs.ModeSticky, true},
		{"a type bit", 0o644 | fs.ModeDir, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := corevfs.ValidatePerm(tc.perm)
			if tc.refused && !kerrs.HasCode(err, corevfs.CodeInvalidPermission) {
				t.Fatalf("ValidatePerm(%v) = %v, want INVALID_PERMISSION", tc.perm, err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("ValidatePerm(%v) = %v, want nil", tc.perm, err)
			}
		})
	}
}

// TestSentinelsCarryTheirAllocatedCode pins every sentinel to the dotted-quad
// value ADR 0056 allocated. The registry audits check that the RANGE is owned
// and that no two codes collide; neither checks that a given sentinel still
// carries the code its documentation names, which is what a consumer's
// errs.HasCode call actually depends on.
func TestSentinelsCarryTheirAllocatedCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sentinel *kerrs.Error
		code     kerrs.Code
		reason   string
	}{
		{"invalid path", corevfs.InvalidPath, corevfs.CodeInvalidPath, "INVALID_PATH"},
		{"invalid permission", corevfs.InvalidPermission, corevfs.CodeInvalidPermission, "INVALID_PERMISSION"},
		{"path escaped", corevfs.PathEscaped, corevfs.CodePathEscaped, "PATH_ESCAPED"},
		{"read failed", corevfs.ReadFailed, corevfs.CodeReadFailed, "READ_FAILED"},
		{"write failed", corevfs.WriteFailed, corevfs.CodeWriteFailed, "WRITE_FAILED"},
		{"publish failed", corevfs.PublishFailed, corevfs.CodePublishFailed, "PUBLISH_FAILED"},
		{"not regular file", corevfs.NotRegularFile, corevfs.CodeNotRegularFile, "NOT_REGULAR_FILE"},
		{"directory not empty", corevfs.DirectoryNotEmpty, corevfs.CodeDirectoryNotEmpty, "DIRECTORY_NOT_EMPTY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.sentinel.Code(); got != tc.code {
				t.Errorf("code = %#x, want %#x", uint32(got), uint32(tc.code))
			}
			if got := tc.sentinel.Reason(); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

// TestNoPublicMessageNamesAPath pins the rule stated in errors.go. A Public is
// wire-safe by contract and therefore reaches third parties; a filesystem path
// is caller data, and often the only piece of it an attacker cannot otherwise
// see. The check is structural — a Public that contains a separator or a
// dot-segment is naming something it should not.
func TestNoPublicMessageNamesAPath(t *testing.T) {
	t.Parallel()
	sentinels := []*kerrs.Error{
		corevfs.InvalidPath, corevfs.InvalidPermission, corevfs.PathEscaped,
		corevfs.ReadFailed, corevfs.WriteFailed, corevfs.PublishFailed,
		corevfs.NotRegularFile, corevfs.DirectoryNotEmpty,
	}
	for _, sentinel := range sentinels {
		public := sentinel.Public()
		for _, banned := range []string{"/", `\`, ".."} {
			if strings.Contains(public, banned) {
				t.Errorf("%s Public %q contains %q — a Public must not carry a path",
					sentinel.Reason(), public, banned)
			}
		}
	}
}
