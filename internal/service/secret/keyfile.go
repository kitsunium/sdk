// Package secret — the machine-local key file: one crypto.Key, created on
// first use, the same key for every process that asks at once.
package secret

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// keyDirMode is the mode the key file's directory is created with when it has
// to be made. An existing directory is left as it is.
const keyDirMode fs.FileMode = 0o700

// keyTempPattern names the temporary a new key is written to before it is
// published by a hard link. It starts with a dot, so it never lists as a name
// anyone chose.
const keyTempPattern string = ".keyfile-*.tmp"

// KeyFile returns the crypto.Key held in the file at path, creating the file
// with a fresh random key the first time — the machine-local key a sealed file
// store (FileConfig.Key) is opened with.
//
// The file holds exactly one key as RAW bytes: exactly crypto.KeyLen of them,
// no encoding, no line ending. `openssl rand 32 > key` writes one. Anything
// else is refused with [KeyFileInvalid] — never truncated, never padded, never
// decoded, because a key reshaped to fit is a different key and a store sealed
// under the original would then read as corrupt.
//
// An absent file is created: crypto/rand bytes are written to a temporary 0600
// file in the same directory, flushed, and PUBLISHED with a hard link, which
// the kernel refuses when the name already exists. So two processes starting
// at once cannot both win: the loser's link fails, it reads the winner's file,
// and both return the same key — which is the property a shared store needs.
// The directory is created 0700 when it has to be made, and flushed after the
// link, so the name survives a crash alongside the bytes.
//
// An existing file with ANY group or world permission bit is refused with
// [InvalidConfig] — the rule the file store's directory follows, and never
// narrowed behind the operator's back. The error never contains the key, and
// never the content of a file that was refused.
//
// It refuses with core/proc.UnsupportedPlatform wherever the file store does —
// today every platform outside the Unix family. On Windows a mode is not an
// access list: 0600 maps to nothing but the read-only attribute and excludes
// no account, so a key file there would promise a protection it does not
// have. Nothing is created where it refuses.
func KeyFile(path string) (key corecrypto.Key, err error) {
	//: no path is no file.
	if path == "" {
		//: InvalidConfig, naming the setting.
		return corecrypto.Key{}, refuseSetting("KeyFile", "empty path")
	}
	//: the same platform gate as the file store, decided before anything is
	//: read or created.
	if !platformNative {
		//: the SDK-wide sentinel.
		return corecrypto.Key{}, coreproc.UnsupportedPlatform
	}
	key, absent, readErr := readKeyFile(path)
	//: the file exists: its key, or its refusal.
	if !absent {
		//: KeyFileInvalid, InvalidConfig, StoreUnavailable — or the key.
		return key, readErr
	}
	//: the first use: create it, or read the one a concurrent caller made.
	return createKeyFile(path)
}

// readKeyFile reads an existing key file, reporting absent when there is none.
// The checks run on the OPENED file, so the file judged is the file read.
func readKeyFile(path string) (key corecrypto.Key, absent bool, err error) {
	file, openErr := os.Open(path)
	//: no file, or one this process cannot open.
	if openErr != nil {
		//: no file: the caller creates one.
		if errors.Is(openErr, fs.ErrNotExist) {
			//: absent, and no error of its own.
			return corecrypto.Key{}, true, nil
		}
		//: StoreUnavailable, naming the operation and never the path.
		return corecrypto.Key{}, false, keyFileUnavailable("open", openErr)
	}
	//: a close failure is reported, but never hides the read's verdict.
	defer func() {
		//: only the first failure is the caller's.
		if closeErr := file.Close(); closeErr != nil && err == nil {
			key, err = corecrypto.Key{}, keyFileUnavailable("close", closeErr)
		}
	}()
	key, err = keyFromOpenFile(file)
	//: the key, or why this file is not one.
	return key, false, err
}

// statter is the part of an opened file a key read needs: its description,
// and its bytes.
type statter interface {
	io.Reader
	// Stat describes the opened file, so the file judged is the file read.
	Stat() (fs.FileInfo, error)
}

// keyFromOpenFile judges and reads an opened key file.
func keyFromOpenFile(file statter) (key corecrypto.Key, err error) {
	info, statErr := file.Stat()
	//: a descriptor that cannot be inspected cannot be trusted.
	if statErr != nil {
		//: StoreUnavailable.
		return corecrypto.Key{}, keyFileUnavailable("stat", statErr)
	}
	//: a directory, a device or a socket where a file belongs.
	if !info.Mode().IsRegular() {
		//: KeyFileInvalid, naming the clause.
		return corecrypto.Key{}, wrapAs(KeyFileInvalid, nil, errs.String("problem", "not a regular file"))
	}
	//: another account can read — or replace — the key.
	if info.Mode().Perm()&otherAccountBits != 0 {
		//: InvalidConfig, the file store's own rule, naming the fix.
		return corecrypto.Key{}, refuseSetting("KeyFile", "accessible to other accounts; chmod 600 it")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, int64(corecrypto.KeyLen)+1))
	defer clear(content)
	//: a read that failed part-way.
	if readErr != nil {
		//: StoreUnavailable.
		return corecrypto.Key{}, keyFileUnavailable("read", readErr)
	}
	//: exactly one key, or not a key file — one byte past the bound is read
	//: so a longer file is refused rather than truncated.
	if len(content) != corecrypto.KeyLen {
		//: KeyFileInvalid, with the length and never the content.
		return corecrypto.Key{}, wrapAs(KeyFileInvalid, nil,
			errs.String("problem", "not exactly one key of raw bytes"), errs.Int("length", len(content)))
	}
	//: NewKey copies, so the buffer is cleared on the way out.
	return corecrypto.NewKey(content)
}

// createKeyFile publishes a fresh key at path unless another caller published
// one first, and returns whichever key the file then holds.
func createKeyFile(path string) (key corecrypto.Key, err error) {
	dir := filepath.Dir(path)
	//: created owner-only when absent; an existing directory is left as it is.
	if mkErr := os.MkdirAll(dir, keyDirMode); mkErr != nil {
		//: StoreUnavailable, naming the operation.
		return corecrypto.Key{}, keyFileUnavailable("mkdir", mkErr)
	}
	raw := make([]byte, corecrypto.KeyLen)
	defer clear(raw)
	//: crypto/rand never returns a short read; its error is reported.
	if _, randErr := rand.Read(raw); randErr != nil {
		//: GenerateFailed, with the entropy source's message.
		return corecrypto.Key{}, wrapAs(GenerateFailed, randErr)
	}
	published, publishErr := publishKey(dir, path, raw)
	//: the name could not be written at all.
	if publishErr != nil {
		//: StoreUnavailable.
		return corecrypto.Key{}, publishErr
	}
	//: another caller won the race: its key is THE key.
	if !published {
		theirs, absent, readErr := readKeyFile(path)
		//: the winner's file vanished between its link and this read.
		if absent {
			//: StoreUnavailable, rather than a second race.
			return corecrypto.Key{}, keyFileUnavailable("read", fs.ErrNotExist)
		}
		//: the winner's key, or why its file is not one.
		return theirs, readErr
	}
	//: this caller's key, now on disk.
	return corecrypto.NewKey(raw)
}

// publishKey writes raw to a flushed temporary beside path and hard-links it
// to path, which the kernel refuses when path exists. It reports false, and
// no error, when another caller's file was there first.
func publishKey(dir, path string, raw []byte) (published bool, err error) {
	temp, createErr := os.CreateTemp(dir, keyTempPattern)
	//: the directory refuses new files.
	if createErr != nil {
		//: StoreUnavailable.
		return false, keyFileUnavailable("create", createErr)
	}
	//: the temporary is closed and removed whatever happens: after a link
	//: the key lives on under its real name, and after a failure nothing
	//: should survive. Either failure is reported, never ignored.
	defer func() {
		cleanupErr := errors.Join(temp.Close(), os.Remove(temp.Name()))
		//: only the first failure is the caller's.
		if cleanupErr != nil && err == nil {
			published, err = false, keyFileUnavailable("cleanup", cleanupErr)
		}
	}()
	//: the bytes and their flush, before the name exists. os.CreateTemp
	//: made the file 0600, so it is never readable by another account.
	if writeErr := writeAndSync(temp, raw); writeErr != nil {
		//: StoreUnavailable.
		return false, writeErr
	}
	linkErr := os.Link(temp.Name(), path)
	//: someone else's key is already there.
	if errors.Is(linkErr, fs.ErrExist) {
		//: not an error: the caller reads theirs.
		return false, nil
	}
	//: a filesystem without hard links, or a directory that refused the name.
	if linkErr != nil {
		//: StoreUnavailable.
		return false, keyFileUnavailable("link", linkErr)
	}
	//: the name must survive a crash with the bytes it names.
	return true, syncDir(dir)
}

// syncer is the part of a fresh temporary a key write needs: its bytes, and
// their flush.
type syncer interface {
	io.Writer
	// Sync flushes what was written to the device.
	Sync() error
}

// writeAndSync writes raw and flushes it to the device.
func writeAndSync(temp syncer, raw []byte) error {
	//: a short or failed write.
	if _, writeErr := temp.Write(raw); writeErr != nil {
		//: StoreUnavailable.
		return keyFileUnavailable("write", writeErr)
	}
	//: the bytes reach the device before the name does.
	if syncErr := temp.Sync(); syncErr != nil {
		//: StoreUnavailable.
		return keyFileUnavailable("sync", syncErr)
	}
	//: written and flushed.
	return nil
}

// syncDir flushes a directory's entries, so a name just linked into it
// survives a power loss.
func syncDir(dir string) (err error) {
	handle, openErr := os.Open(dir)
	//: a directory that cannot be reopened cannot be flushed.
	if openErr != nil {
		//: StoreUnavailable.
		return keyFileUnavailable("sync", openErr)
	}
	//: a close failure leaves the handle leaked; it is reported.
	defer func() {
		//: only the first failure is the caller's.
		if closeErr := handle.Close(); closeErr != nil && err == nil {
			err = keyFileUnavailable("sync", closeErr)
		}
	}()
	//: the directory entry reaches the device.
	if syncErr := handle.Sync(); syncErr != nil {
		//: StoreUnavailable.
		return keyFileUnavailable("sync", syncErr)
	}
	//: durable.
	return nil
}

// keyFileUnavailable is the retryable verdict for one key-file operation. The
// cause travels as a field only for the operation's name: an *os.PathError
// message carries the path, which is not repeated.
func keyFileUnavailable(operation string, cause error) error {
	//: the operation and whether the cause was an absence, never the path.
	return errs.Wrap(coresecret.StoreUnavailable, errs.WrapParams{},
		errs.String("store", "keyfile"), errs.String("operation", operation),
		errs.Bool("not_exist", errors.Is(cause, fs.ErrNotExist)),
		errs.Bool("permission", errors.Is(cause, fs.ErrPermission)))
}
