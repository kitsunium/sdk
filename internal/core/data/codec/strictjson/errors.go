package strictjson

// exitDataErr matches sysexits EX_DATAERR (65): the input was wrong.
const exitDataErr int = 65

// exitIOErr matches sysexits EX_IOERR (74): the input could not be read.
const exitIOErr int = 74

// exitSoftware matches sysexits EX_SOFTWARE (70): the caller's code is wrong.
const exitSoftware int = 70

// The HTTP statuses the refusals carry, as RFC 9110 numbers them. Literals,
// because the core does not import net/http for a number (ADR 0160); each
// is the value of the net/http constant its comment names.
const (
	// httpBadRequest is 400, http.StatusBadRequest: the document is wrong.
	httpBadRequest int = 400
	// httpContentTooLarge is 413, http.StatusRequestEntityTooLarge: the
	// document is longer than the bound.
	httpContentTooLarge int = 413
	// httpUnsupportedMediaType is 415, http.StatusUnsupportedMediaType: the
	// body does not declare itself JSON.
	httpUnsupportedMediaType int = 415
)
