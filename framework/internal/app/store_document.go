package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The documents a store's engine works on (ADR 0179 §2): a record's JSON as
// it rests — byte for byte what kit encodes —, and the entity once decoded,
// kept beside it as an any so that a key, an index or a read does not decode
// it twice (plug.DocumentValue). The engine is compiled once, whatever the
// entity type — the store subsystem instantiates the SDK's document store at
// one record type —, and what the product declares on its type reaches it
// as functions of a document (engineConfig).

// errVersionsNotKept is the refusal of an engine of kit's core that wraps
// one keeping no versions: static data, as an engine's sentinel was — a
// call that refuses allocates nothing (ADR 0176).
var errVersionsNotKept error = &plug.RefusalError{Kind: plug.RefusalVersionsNotKept, Err: &Error{
	Status: http.StatusBadRequest, Code: WireInvalid, Message: "the store keeps no versions: declare app.Revisions",
}}

// undecodable is a record that does not decode into its store's type, as
// an engine refuses one: naming the store and what failed, never a byte of
// the record.
func undecodable(store string, err error) error {
	return plug.Refuse(plug.RefusalUndecodable, failure(CodeStoreDecode, "STORE_DECODE", "a stored entity does not match its type", nil,
		errs.String("store", store), errs.String("cause", jsonCause(err))))
}

// unencodable is an entity that does not encode, as an engine refuses one:
// naming the store and what failed, never what the entity held.
func unencodable(store string, err error) error {
	return plug.Refuse(plug.RefusalUnencodable, failure(CodeStoreEncode, "STORE_ENCODE", "the entity cannot be stored", nil,
		errs.String("store", store), errs.String("cause", encodeCause(err))))
}

// kitRefusal is a refusal of kind kit's core makes itself — an engine of its
// own that wraps the subsystem's, a file of its own —, for the store named
// store: as an engine's, which its caller words (Store.said), and in kit's
// words already where it is not.
func kitRefusal(kind plug.Refusal, store string) error {
	var said *Error
	switch kind {
	case plug.RefusalNotFound:
		said = NotFound(store + ": no record has this key")
	case plug.RefusalNotTrashed:
		said = NotFound(store + ": no record is in the trash under this key")
	case plug.RefusalExists:
		said = Conflict(store + ": a record already has this key")
	case plug.RefusalKeyEmpty:
		said = Invalid(store + ": an entity's key cannot be empty")
	case plug.RefusalVersionsNotKept:
		said = Invalid(store + " keeps no versions: declare app.Revisions")
	default:
		said = Unavailable(store + ": the store refused the call")
	}
	return plug.Refuse(kind, said)
}

// jsonCause describes a decoding failure without a byte of the record:
// encoding/json quotes the offending character of a syntax error.
func jsonCause(err error) string {
	if typeErr, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		typeName := "an unknown type"
		if typeErr.Type != nil {
			typeName = typeErr.Type.String()
		}
		return "the stored value of field " + typeErr.Field + " does not fit " + typeName
	}
	if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
		return "malformed JSON at offset " + strconv.FormatInt(syntaxErr.Offset, 10)
	}
	return fmt.Sprintf("%T", err)
}

// encodeCause describes an encoding failure without the value that failed.
func encodeCause(err error) string {
	if unsupported, ok := errors.AsType[*json.UnsupportedTypeError](err); ok {
		return "unsupported type " + unsupported.Type.String()
	}
	if marshalerErr, ok := errors.AsType[*json.MarshalerError](err); ok {
		return "the marshaler of " + marshalerErr.Type.String() + " failed"
	}
	return fmt.Sprintf("%T", err)
}
