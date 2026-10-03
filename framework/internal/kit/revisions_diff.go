// Package kit — what changed between two versions, and a version written back.
package kit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/codec/jsonpatch"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

const (
	// secretMark starts a token as it lies in a document: a JSON string
	// whose text starts with a NUL, which no member kit wrote holds.
	secretMark = `\u0000kit-secret:`
	// secretToken starts a token as it is decoded.
	secretToken = "\x00kit-secret:"
)

// What changed between two versions, and a version written back (ADR 0007
// §3): Diff is the SDK's jsonpatch.Diff of the two versions' JSON, a secret
// member saying it changed and never what; Restore writes a version back —
// whole, or some fields — as a new version, keeping every secret member and
// the workflow's state.

// diff is what changed from version from to version to of the record under
// key.
func (s *StoreService[T]) diff(ctx context.Context, key string, from, to uint64) ([]Edit, error) {
	all, err := s.openedVersions(ctx, key)
	if err != nil {
		return nil, err
	}
	var a, b []byte
	for _, v := range all {
		if v.Number == from {
			a = v.JSON
		}
		if v.Number == to {
			b = v.JSON
		}
	}
	switch {
	case a == nil:
		return nil, s.noVersion(key, from)
	case b == nil:
		return nil, s.noVersion(key, to)
	}
	edits, err := s.plan().diffWithoutSecrets(a, b)
	if err != nil {
		return nil, failure(CodeRevisionRead, "REVISIONS_DIFF", "two versions of a record could not be compared", err, errs.String("store", s.id))
	}
	return edits, nil
}

// diffWithoutSecrets is what changed from the document from to the document
// to, both of the plan's type: the SDK's operations, a secret member's
// values left out — an edit at a secret member has neither, and an edit
// whose value holds one holds it no more —, so that it says the member
// changed, never what.
func (p *classPlan) diffWithoutSecrets(from, to []byte) ([]Edit, error) {
	tok, err := newSecretTokens()
	if err != nil {
		return nil, err
	}
	a, err := tok.tokenized(from, p.rules)
	if err != nil {
		return nil, err
	}
	b, err := tok.tokenized(to, p.rules)
	if err != nil {
		return nil, err
	}
	edits, err := jsonpatch.Diff(a, b)
	if err != nil {
		return nil, err
	}
	out := make([]Edit, 0, len(edits))
	for _, e := range edits {
		out = append(out, Edit{Op: string(e.Op), Path: e.Path, From: tok.scrub(e.Old), To: tok.scrub(e.Value)})
	}
	return out, nil
}

// secretTokens stand for secret members' values in a diff: each value is
// replaced by a token made of a keyed hash of it, under a key made for the
// one diff, so that two equal values give one token and two different ones
// two — the diff then says where a secret changed — and the value is never
// read back: every token is taken out of what the diff returns.
type secretTokens struct{ key []byte }

// newSecretTokens makes the tokens of one diff, under a key of their own.
func newSecretTokens() (*secretTokens, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &secretTokens{key: key}, nil
}

// token is the token of a secret member's value.
func (t *secretTokens) token(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, t.key)
	mac.Write(raw)
	return secretToken + hex.EncodeToString(mac.Sum(nil)), nil
}

// tokenized is doc, a JSON document of the rules' type, its secret members'
// values replaced by their tokens; numbers are kept as written.
func (t *secretTokens) tokenized(doc []byte, r *rules) ([]byte, error) {
	v, err := decodeNumbers(doc)
	if err != nil {
		return nil, err
	}
	if err := t.replace(v, r); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// replace puts tokens in v, a decoded JSON value, where r says a member is
// secret, as deep as v goes.
func (t *secretTokens) replace(v any, r *rules) error {
	if r == nil {
		return nil
	}
	if r.elem != nil {
		for _, e := range jsonElems(v) {
			if err := t.replace(e, r.elem); err != nil {
				return err
			}
		}
		return nil
	}
	switch obj := v.(type) {
	case map[string]any:
		return t.replaceMembers(obj, r)
	case []any:
		// A list where the rules want an object holds no member of theirs.
	}
	return nil
}

// replaceMembers puts tokens in obj, a decoded JSON object, where r says a
// member is secret, as deep as obj goes.
func (t *secretTokens) replaceMembers(obj map[string]any, r *rules) error {
	for _, f := range r.fields {
		val, present := obj[f.name]
		if !present {
			continue
		}
		if f.tag.effective() != model.ClassSecret {
			if err := t.replace(val, f.sub); err != nil {
				return err
			}
			continue
		}
		tok, err := t.token(val)
		if err != nil {
			return err
		}
		obj[f.name] = tok
	}
	return nil
}

// scrub is a value of an edit without a secret: none when it is one — the
// edit says the member changed —, and the secret members of an object it
// holds left out, as an export leaves them out.
func (t *secretTokens) scrub(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !bytes.Contains(raw, []byte(secretMark)) {
		return raw
	}
	v, err := decodeNumbers(raw)
	if s, ok := v.(string); err != nil || ok && isToken(s) {
		return nil
	}
	out, err := json.Marshal(withoutTokens(v))
	if err != nil {
		return nil
	}
	return out
}

// isToken reports whether a decoded JSON string is a secret's token.
func isToken(s string) bool { return strings.HasPrefix(s, secretToken) }

// withoutTokens is v without the tokens it holds: a member that is one is
// left out, an element that is one is null.
func withoutTokens(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if holdsToken(e) {
				delete(x, k)
				continue
			}
			x[k] = withoutTokens(e)
		}
	case []any:
		for i, e := range x {
			x[i] = nil
			if !holdsToken(e) {
				x[i] = withoutTokens(e)
			}
		}
	}
	return v
}

// holdsToken reports whether a decoded JSON value is a secret's token.
func holdsToken(v any) bool {
	switch x := v.(type) {
	case string:
		return isToken(x)
	case map[string]any, []any:
		return false
	}
	return false
}

// decodeNumbers decodes a JSON document, its numbers kept as written.
func decodeNumbers(doc []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
