// Package secret — the file store's on-disk record: one JSON document per
// secret, sealed when the store has a key.
package secret

import (
	"encoding/json"
	"time"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// recordFormat is the one record format this build writes and reads. A record
// carrying another is refused rather than guessed at.
const recordFormat int = 1

// recordSuffix names a record file: "<name>.secret". Nothing else the
// directory holds — the lock files, a publication's temporary — ends with it.
const recordSuffix string = ".secret"

// recordAADPrefix is the associated data every sealed record binds, followed
// by the secret's name: a record copied under another name, or a box sealed
// by another program under the same key, does not open.
const recordAADPrefix string = "kitsunium/secret file record v1\x00"

// sealAlgorithm is the AEAD a record is sealed with: the SDK's default, which
// the blank import in keyring.go activates.
const sealAlgorithm corecrypto.Algorithm = "aes-256-gcm"

// fileRecord is one secret's whole history, as written to disk. One document
// per secret is what makes a Put or a Prune ONE atomic publication: a reader
// sees the history before or after, never a version without its neighbours.
type fileRecord struct {
	// Format is recordFormat.
	Format int `json:"format"`
	// Name is the secret's name, checked against the file it was read from,
	// so a record renamed on disk is refused even when it is not sealed.
	Name string `json:"name"`
	// Versions is the kept history, newest first.
	Versions []fileVersion `json:"versions"`
}

// fileVersion is one version as written to disk.
type fileVersion struct {
	// Version is the version's number.
	Version int `json:"version"`
	// Created is the store's stamp, in UTC.
	Created time.Time `json:"created"`
	// Value is the secret's bytes, base64 in the JSON document. The document
	// is written 0600 and, when the store has a key, never in the clear.
	Value []byte `json:"value"`
}

// recordPath is the path of name's record inside the store's root.
func recordPath(name string) string {
	//: a valid name is a valid single path element: no separator, no dot.
	return name + recordSuffix
}

// recordAAD is the associated data a sealed record of name binds.
func recordAAD(name string) []byte {
	//: the prefix carries a NUL, so no name can extend it into another.
	return []byte(recordAADPrefix + name)
}

// encodeRecord renders a newest-first history as the bytes to publish, sealed
// when key is set. The plaintext is cleared once it has been sealed, so the
// only copy of the document in memory is the one that is about to be written.
func encodeRecord(name string, versions []coresecret.VersionValue, key corecrypto.Key, sealed bool) (encoded []byte, err error) {
	record := fileRecord{Format: recordFormat, Name: name, Versions: make([]fileVersion, 0, len(versions))}
	//: each version's bytes are revealed into the document, and nowhere else.
	for _, version := range versions {
		record.Versions = append(record.Versions, fileVersion{
			Version: version.Version,
			Created: version.Created.UTC(),
			Value:   version.Value.Reveal(),
		})
	}
	document, marshalErr := json.Marshal(record)
	clearRecord(record)
	//: a history of ints, times and byte slices always marshals; a failure
	//: here is the runtime's, and its message is not repeated.
	if marshalErr != nil {
		//: StoreUnavailable, naming the operation.
		return nil, errs.Wrap(coresecret.StoreUnavailable, errs.WrapParams{},
			errs.String("secret", name), errs.String("operation", "encode"))
	}
	//: an unsealed store writes the document as it is.
	if !sealed {
		//: 0600, and the caller chose no key.
		return document, nil
	}
	box, sealErr := corecrypto.Seal(sealAlgorithm, key, document, recordAAD(name))
	clear(document)
	//: sealing fails only on a scheme that is not registered or a key that is
	//: not one, both of which construction refused.
	if sealErr != nil {
		//: StoreUnavailable, with the crypto verdict's text: it names no key.
		return nil, wrapAs(coresecret.StoreUnavailable, sealErr, errs.String("secret", name), errs.String("operation", "seal"))
	}
	//: the sealed document — the only form a keyed store writes.
	return box, nil
}

// decodeRecord reads the bytes of name's record back into a newest-first
// history, opening them first when the store is sealed. Every failure is
// RecordUnreadable, and none of them repeats the decoder's message: that
// message can quote the document, and the document is the secret.
func decodeRecord(name string, data []byte, key corecrypto.Key, sealed bool) (versions []coresecret.VersionValue, err error) {
	document := data
	//: a keyed store only ever wrote sealed records.
	if sealed {
		opened, openErr := corecrypto.Open(key, data, recordAAD(name))
		//: tampered, truncated, renamed, sealed under another key, or never
		//: sealed at all — one verdict.
		if openErr != nil {
			//: RecordUnreadable, naming the secret.
			return nil, unreadable(name, "the record does not open under this store's key")
		}
		document = opened
		//: the opened plaintext is cleared once decoded.
		defer clear(opened)
	}
	var record fileRecord
	//: a sealed record read without a key, or a damaged document.
	if json.Unmarshal(document, &record) != nil {
		//: RecordUnreadable; the decoder's message is dropped.
		return nil, unreadable(name, "the record is not a document this store wrote")
	}
	defer clearRecord(record)
	//: the document must describe the secret whose file it is, in the format
	//: this build writes.
	if record.Format != recordFormat || record.Name != name {
		//: RecordUnreadable.
		return nil, unreadable(name, "the record's format or name does not match its file")
	}
	//: the history itself, checked and converted.
	return historyOf(name, record)
}

// historyOf converts a decoded record into VersionValues, refusing a history
// this store could not have written: empty, an empty value, a non-positive
// number, or numbers that do not strictly decrease.
func historyOf(name string, record fileRecord) (versions []coresecret.VersionValue, err error) {
	//: every name on disk holds at least one version — Prune keeps one.
	if len(record.Versions) == 0 {
		//: RecordUnreadable.
		return nil, unreadable(name, "the record holds no version")
	}
	versions = make([]coresecret.VersionValue, 0, len(record.Versions))
	previous := 0
	//: newest first, strictly decreasing, every one a non-empty secret.
	for index, version := range record.Versions {
		//: out of order, repeated, non-positive or empty.
		if version.Version < 1 || len(version.Value) == 0 || (index > 0 && version.Version >= previous) {
			//: RecordUnreadable.
			return nil, unreadable(name, "the record's history is not one this store writes")
		}
		previous = version.Version
		versions = append(versions, coresecret.VersionValue{
			Name:    name,
			Version: version.Version,
			Value:   coresecret.NewValue(version.Value),
			Created: version.Created,
		})
	}
	//: the history, newest first.
	return versions, nil
}

// unreadable is the RecordUnreadable verdict for name, with the clause.
func unreadable(name, problem string) error {
	//: the name and the clause; never a byte of the record.
	return wrapAs(RecordUnreadable, nil, errs.String("secret", name), errs.String("problem", problem))
}

// clearRecord overwrites every secret the decoded or encoded document holds.
// The Values handed out are copies, so this only erases the transient buffers
// the JSON round trip created.
func clearRecord(record fileRecord) {
	//: each version's revealed or decoded bytes.
	for _, version := range record.Versions {
		clear(version.Value)
	}
}
