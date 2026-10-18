package queue

// exitDataErr matches sysexits EX_DATAERR (65). A failure a handler declares
// no retry can fix is almost always the message itself — a payload that does
// not decode, a reference to something since deleted — and the same bytes are
// refused the same way however often they are delivered.
const exitDataErr int = 65
