// Package docstore — range 0.3.80.* (ADR 0110 service/data/docstore block;
// ADR 0139 added 16 and 17, ADR 0143 18 to 20). Both engines answer every one
// of them alike, and the range keeps the LL = 3 its engine allocated it under
// now that it is declared here (ADR 0160).
//
// Package docstore declares the document-store domain: typed, keyed JSON
// documents with unique and multi-valued secondary indexes, kept by two
// engines under one contract — the file engine in memory or through
// core/data/vfs (ADR 0110), the SQL engine in a database the caller owns
// (ADR 0139), both keeping a document's versions in its own write when asked
// (ADR 0143).
//
// It holds what the two engines share and nothing only one of them has:
//
//   - the ports. [Collection] is a store whose calls take no context, which
//     the file engine is; [CollectionContext] is the same calls, each taking
//     a context first, which the SQL engine is. [Versioned] and
//     [VersionedContext] are their siblings for a document's versions, and
//     [Announcer] is the pair of hooks both engines answer exactly as they
//     are;
//   - the values both engines read and return — [EntryValue], [VersionValue],
//     [StampValue] — and [IndexSpec], the declaration both take, with its two
//     constructors [Unique] and [Index];
//   - the codes of the 0.3.80.* range and their sentinels: both engines answer
//     the same refusals under the same codes (ADR 0160).
//
// There are two ports and not one because the engines differ on the one
// thing a single interface would have to fix: the file engine waits on
// nothing a caller could abandon and takes no context (ADR 0110 §D6), while
// every call of the SQL engine waits on a database and takes one. A single
// port would give the file engine a context it cannot honour, or take the SQL
// engine's away (ADR 0139 §D2). So each port is the contract of one shape of
// call, with the same methods, the same documents and the same refusals.
//
// The engines, their configurations, the file engine's statistics and every
// statement live in internal/service/data/docstore.
//
// Package docstore — the siblings of Collection (ADR 0039): a document's
// versions, and the same two ports with a context on every call, which the SQL
// engine implements.
//
// Package docstore — declares the sentinel *errs.Error outcomes both engines
// answer. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
//
// No Public and no Private below carries a key or a document. A store key is
// routinely an e-mail address or the hash of a token, and an index key is
// usually exactly the value a caller must not learn back from a refusal: the
// fields name the index and the operation, never what was looked up.
//
// The HTTP statuses are spelled as the integers errs.WithHTTPStatus takes,
// each named after its status: the core imports no net/http to read five
// numbers (ADR 0160).
//
// Package docstore — the declaration of a secondary index, which both engines
// take at construction and file by the same rule.
//
// Package docstore — the values both engines read and return: a stored
// document as JSON, one version of a document, and what a write says about the
// version it makes.
package docstore
