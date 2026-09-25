//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package secret_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// markerSecret is written by the encryption tests and hunted for on disk.
const markerSecret string = "PLAINTEXT-MARKER-7f3a9c-do-not-write"

// TestFileStorePermissions pins the modes: the directory is created 0700 and
// every record is published 0600, whatever the umask.
func TestFileStorePermissions(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "secrets")
	store := newFileStore(t, svcsecret.FileConfig{Dir: dir})
	if _, err := store.Put(t.Context(), "api-token", coresecret.FromString("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %o, want 700", got)
	}
	recordInfo, err := os.Stat(filepath.Join(dir, "api-token.secret"))
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if got := recordInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("record mode = %o, want 600", got)
	}
}

// TestFileStoreRefusesADirectoryOthersCanRead pins that an existing directory
// is checked and refused, never narrowed behind the operator's back.
func TestFileStoreRefusesADirectoryOthersCanRead(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		mode fs.FileMode
	}
	tests := []tc{
		{"group-readable", 0o750},
		{"world-readable", 0o705},
		{"group-writable", 0o770},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "secrets")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(dir, c.mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		_, err := svcsecret.NewFile(svcsecret.FileConfig{Dir: dir})
		if !errs.HasCode(err, svcsecret.CodeInvalidConfig) {
			t.Fatalf("NewFile(%o) = %v, want InvalidConfig", c.mode, err)
		}
		info, statErr := os.Stat(dir)
		if statErr != nil || info.Mode().Perm() != c.mode {
			t.Errorf("the refused directory's mode changed to %o", info.Mode().Perm())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFileStoreRefusesAnUnusableConfig pins the construction refusals that
// need no directory at all.
func TestFileStoreRefusesAnUnusableConfig(t *testing.T) {
	t.Parallel()
	if _, err := svcsecret.NewFile(svcsecret.FileConfig{}); !errs.HasCode(err, svcsecret.CodeInvalidConfig) {
		t.Errorf("NewFile(no Dir) = %v, want InvalidConfig", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svcsecret.NewFile(svcsecret.FileConfig{Dir: file}); !errs.HasCode(err, svcsecret.CodeInvalidConfig) {
		t.Errorf("NewFile(a file) = %v, want InvalidConfig", err)
	}
}

// TestSealedFileStoreWritesNoPlaintext is the at-rest claim: with a key,
// nothing readable reaches the disk — not the value, not its base64 or hex,
// not the version history.
func TestSealedFileStoreWritesNoPlaintext(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets")
	store := newFileStore(t, svcsecret.FileConfig{Dir: dir, Key: testKey(t)})
	for range 3 {
		if _, err := store.Put(t.Context(), "stripe-key", coresecret.FromString(markerSecret)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := store.Prune(t.Context(), "stripe-key", 2); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	forbidden := [][]byte{
		[]byte(markerSecret),
		[]byte(base64.StdEncoding.EncodeToString([]byte(markerSecret))),
		[]byte(hex.EncodeToString([]byte(markerSecret))),
		[]byte(`"versions"`),
		[]byte(`"created"`),
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		content, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Name(), readErr)
		}
		for _, needle := range forbidden {
			if bytes.Contains(content, needle) {
				t.Fatalf("%s holds %q in the clear", entry.Name(), needle)
			}
		}
	}
	//: and the store still reads its own records back.
	current, err := store.Get(t.Context(), "stripe-key")
	if err != nil || current.Value.RevealString() != markerSecret || current.Version != 3 {
		t.Fatalf("Get = (v%d, %v)", current.Version, err)
	}
}

// TestUnsealedFileStoreRecordIsNotHumanReadableButIsNotEncrypted documents the
// other half honestly: without a key the record is base64 in a file only its
// owner can read, and that is all.
func TestUnsealedFileStoreRecordIsNotHumanReadableButIsNotEncrypted(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets")
	store := newFileStore(t, svcsecret.FileConfig{Dir: dir})
	if _, err := store.Put(t.Context(), "plain", coresecret.FromString(markerSecret)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "plain.secret"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(content, []byte(base64.StdEncoding.EncodeToString([]byte(markerSecret)))) {
		t.Fatalf("the unsealed record is not the documented base64 document: %s", content)
	}
}

// TestFileStoreRefusesARecordItCannotRead pins RecordUnreadable for the three
// ways a record goes wrong: another key, no key, and a record renamed to
// answer for another secret.
func TestFileStoreRefusesARecordItCannotRead(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets")
	writer := newFileStore(t, svcsecret.FileConfig{Dir: dir, Key: testKey(t)})
	for _, name := range []string{"alpha", "beta"} {
		if _, err := writer.Put(t.Context(), name, coresecret.FromString(name+"-value")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	otherKey := newFileStore(t, svcsecret.FileConfig{Dir: dir, Key: testKey(t)})
	if _, err := otherKey.Get(t.Context(), "alpha"); !errs.HasCode(err, svcsecret.CodeRecordUnreadable) {
		t.Errorf("Get under another key = %v, want RecordUnreadable", err)
	}
	noKey := newFileStore(t, svcsecret.FileConfig{Dir: dir})
	if _, err := noKey.Get(t.Context(), "alpha"); !errs.HasCode(err, svcsecret.CodeRecordUnreadable) {
		t.Errorf("Get without a key = %v, want RecordUnreadable", err)
	}
	//: beta's record copied over alpha's name must not answer for alpha.
	beta, err := os.ReadFile(filepath.Join(dir, "beta.secret"))
	if err != nil {
		t.Fatalf("read beta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.secret"), beta, 0o600); err != nil {
		t.Fatalf("overwrite alpha: %v", err)
	}
	if _, err := writer.Get(t.Context(), "alpha"); !errs.HasCode(err, svcsecret.CodeRecordUnreadable) {
		t.Errorf("Get of a record renamed from beta = %v, want RecordUnreadable", err)
	}
	//: and a Put never writes over a record the store cannot read.
	if _, err := writer.Put(t.Context(), "alpha", coresecret.FromString("x")); !errs.HasCode(err, svcsecret.CodeRecordUnreadable) {
		t.Errorf("Put over an unreadable record = %v, want RecordUnreadable", err)
	}
	//: the refusal names the secret and quotes no byte of the record.
	_, err = writer.Get(t.Context(), "alpha")
	if strings.Contains(errs.PrivateOf(err)+err.Error(), "beta-value") {
		t.Errorf("the refusal quotes the record: %v", err)
	}
}

// TestFileStorePublishesAtomically pins what a concurrent reader sees while
// another goroutine rewrites the record: a whole history every time, never a
// torn one, and no temporary left behind afterwards.
func TestFileStorePublishesAtomically(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets")
	store := newFileStore(t, svcsecret.FileConfig{Dir: dir, Key: testKey(t)})
	if _, err := store.Put(t.Context(), "rotating", coresecret.FromString("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	const writes int = 40
	done := make(chan struct{})
	var readErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := store.Versions(t.Context(), "rotating"); err != nil {
				readErr = err
				return
			}
		}
	})
	for range writes {
		if _, err := store.Put(t.Context(), "rotating", coresecret.FromString("next")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	close(done)
	wg.Wait()
	if readErr != nil {
		t.Fatalf("a reader saw %v while the record was being replaced", readErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a publication temporary survived: %s", entry.Name())
		}
	}
	current, err := store.Get(t.Context(), "rotating")
	if err != nil || current.Version != writes+1 {
		t.Fatalf("Get = (v%d, %v), want v%d", current.Version, err, writes+1)
	}
}

// TestFileStoreSerialisesWritersAcrossInstances pins the cross-process claim
// the way one process can: two stores over one directory each hold their own
// lock descriptor, exactly as two processes would, and concurrent Puts through
// both must still mint every number exactly once.
func TestFileStoreSerialisesWritersAcrossInstances(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets")
	first := newFileStore(t, svcsecret.FileConfig{Dir: dir})
	second := newFileStore(t, svcsecret.FileConfig{Dir: dir})
	const perWriter int = 12
	var wg sync.WaitGroup
	failures := make(chan error, 4*perWriter)
	for _, store := range []coresecret.Store{first, second, first, second} {
		wg.Go(func() {
			for range perWriter {
				if _, err := store.Put(t.Context(), "contended", coresecret.FromString("x")); err != nil {
					failures <- err
				}
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("Put: %v", err)
	}
	versions, err := first.Versions(t.Context(), "contended")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 4*perWriter || versions[0].Version != 4*perWriter {
		t.Fatalf("%d versions, newest %d; want %d each — two writers minted one number",
			len(versions), versions[0].Version, 4*perWriter)
	}
}

// TestFileStoreListsOnlyRecords pins that the lock files and anything else in
// the directory never appear as secrets.
func TestFileStoreListsOnlyRecords(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets")
	store := newFileStore(t, svcsecret.FileConfig{Dir: dir})
	for _, name := range []string{"zeta", "alpha"} {
		if _, err := store.Put(t.Context(), name, coresecret.FromString("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	for _, stray := range []string{"README", "Upper.secret", ".hidden.secret", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, stray), []byte("x"), 0o600); err != nil {
			t.Fatalf("write stray: %v", err)
		}
	}
	names, err := store.Names(t.Context())
	if err != nil || strings.Join(names, ",") != "alpha,zeta" {
		t.Fatalf("Names = (%v, %v), want [alpha zeta]", names, err)
	}
}

// TestFileStoreAfterClose pins that a closed store answers every call with the
// retryable StoreUnavailable rather than a panic or a silent empty answer.
func TestFileStoreAfterClose(t *testing.T) {
	t.Parallel()
	store, err := svcsecret.NewFile(svcsecret.FileConfig{Dir: filepath.Join(t.TempDir(), "secrets")})
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	if _, putErr := store.Put(t.Context(), "closed", coresecret.FromString("x")); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}
	if closeErr := store.(io.Closer).Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	if _, getErr := store.Get(t.Context(), "closed"); !errs.HasCode(getErr, coresecret.CodeStoreUnavailable) {
		t.Errorf("Get after Close = %v, want StoreUnavailable", getErr)
	}
	if _, namesErr := store.Names(t.Context()); !errs.HasCode(namesErr, coresecret.CodeStoreUnavailable) {
		t.Errorf("Names after Close = %v, want StoreUnavailable", namesErr)
	}
	if _, putErr := store.Put(t.Context(), "closed", coresecret.FromString("y")); !errs.HasCode(putErr, coresecret.CodeStoreUnavailable) {
		t.Errorf("Put after Close = %v, want StoreUnavailable", putErr)
	}
}
