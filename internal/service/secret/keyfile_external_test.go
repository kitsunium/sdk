//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package secret_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// TestKeyFileConcurrentFirstUseAgreesOnOneKey is the property a shared store
// needs: callers racing to create the key file all return THE SAME key, the
// file is 0600 in a directory created 0700, and no temporary survives.
func TestKeyFileConcurrentFirstUseAgreesOnOneKey(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "secrets", "dev")
	path := filepath.Join(dir, "store.key")
	const callers int = 16
	keys := make([][]byte, callers)
	failures := make([]error, callers)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for index := range callers {
		done.Go(func() {
			start.Wait()
			key, err := svcsecret.KeyFile(path)
			keys[index], failures[index] = key.Bytes(), err
		})
	}
	start.Done()
	done.Wait()
	for index := range callers {
		if failures[index] != nil {
			t.Fatalf("caller %d: %v", index, failures[index])
		}
		if !bytes.Equal(keys[index], keys[0]) {
			t.Fatalf("caller %d returned a different key: two callers each created their own", index)
		}
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(onDisk, keys[0]) || len(onDisk) != corecrypto.KeyLen {
		t.Fatalf("the file holds %d bytes (%v), not the key every caller returned", len(onDisk), err)
	}
	assertMode(t, path, 0o600)
	assertMode(t, dir, 0o700)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the directory holds %d entries (%v): a temporary survived", len(entries), err)
	}
	//: and a later call reads the same key back.
	again, err := svcsecret.KeyFile(path)
	if err != nil || !bytes.Equal(again.Bytes(), keys[0]) {
		t.Fatalf("a second use returned (%v): not the key on disk", err)
	}
}

// TestKeyFileRefusesWhatIsNotOneKey pins that content is never reshaped and
// never repeated in the refusal.
func TestKeyFileRefusesWhatIsNotOneKey(t *testing.T) {
	t.Parallel()
	marker := bytes.Repeat([]byte("k"), corecrypto.KeyLen)
	type tc struct {
		name    string
		content []byte
	}
	tests := []tc{
		{"empty", nil},
		{"one byte short", marker[:corecrypto.KeyLen-1]},
		{"one byte long", append(bytes.Clone(marker), 'k')},
		{"a key with a line ending", append(bytes.Clone(marker), '\n')},
		{"base64 text is not raw bytes", []byte(base64.StdEncoding.EncodeToString(marker))},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "store.key")
		if err := os.WriteFile(path, c.content, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := svcsecret.KeyFile(path)
		if !errs.HasCode(err, svcsecret.CodeKeyFileInvalid) {
			t.Fatalf("%s: KeyFile = %v, want KEY_FILE_INVALID", c.name, err)
		}
		if strings.Contains(err.Error()+errs.PrivateOf(err)+fieldText(err), "kkkk") {
			t.Errorf("%s: the refusal repeats the file's content", c.name)
		}
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(after, c.content) {
			t.Errorf("%s: a refused file was changed", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestKeyFileRefusesAFileOthersCanRead pins the permission rule — the file
// store directory's own — and the other refusals that need no content.
func TestKeyFileRefusesAFileOthersCanRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	readable := filepath.Join(dir, "readable.key")
	if err := os.WriteFile(readable, bytes.Repeat([]byte{7}, corecrypto.KeyLen), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(readable, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := svcsecret.KeyFile(readable); !errs.HasCode(err, svcsecret.CodeInvalidConfig) {
		t.Errorf("KeyFile(0640) = %v, want INVALID_CONFIG", err)
	}
	assertMode(t, readable, 0o640)
	if _, err := svcsecret.KeyFile(dir); !errs.HasCode(err, svcsecret.CodeKeyFileInvalid) {
		t.Errorf("KeyFile(a directory) = %v, want KEY_FILE_INVALID", err)
	}
	if _, err := svcsecret.KeyFile(""); !errs.HasCode(err, svcsecret.CodeInvalidConfig) {
		t.Errorf("KeyFile(\"\") = %v, want INVALID_CONFIG", err)
	}
	existing := filepath.Join(dir, "existing.key")
	want := bytes.Repeat([]byte{9}, corecrypto.KeyLen)
	if err := os.WriteFile(existing, want, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	key, err := svcsecret.KeyFile(existing)
	if err != nil || !bytes.Equal(key.Bytes(), want) {
		t.Errorf("KeyFile(an existing key) = (%v): not the file's key", err)
	}
}

// TestAKeyFileOpensTheSealedStoreItWasMadeFor is the intended use, end to
// end: the store sealed under a KeyFile key reopens under the key read back.
func TestAKeyFileOpensTheSealedStoreItWasMadeFor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	keyPath := filepath.Join(root, "store.key")
	storeDir := filepath.Join(root, "store")
	first, err := svcsecret.KeyFile(keyPath)
	if err != nil {
		t.Fatalf("KeyFile: %v", err)
	}
	writer := newFileStore(t, svcsecret.FileConfig{Dir: storeDir, Key: first})
	if _, putErr := writer.Put(t.Context(), "api-token", fromText("sealed")); putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}
	second, err := svcsecret.KeyFile(keyPath)
	if err != nil {
		t.Fatalf("KeyFile again: %v", err)
	}
	reader := newFileStore(t, svcsecret.FileConfig{Dir: storeDir, Key: second})
	current, err := reader.Get(t.Context(), "api-token")
	if err != nil || current.Value.RevealString() != "sealed" {
		t.Fatalf("Get under the key read back = (%v)", err)
	}
}

// assertMode fails unless path carries exactly mode's permission bits.
func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Errorf("mode of %s = %o, want %o", filepath.Base(path), got, mode)
	}
}
