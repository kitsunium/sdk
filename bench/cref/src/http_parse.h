// http_parse.h - the HTTP/1.1 request parser the C reference measures, with
// exactly the scope and refusals of the SDK's restricted http1 profile (plan
// S5). It is stateless: a caller that got HTTP_PARTIAL appends bytes and calls
// again from the start of the request.
//
// Scope:
//   - request line `token SP target SP HTTP/1.1 CRLF`; no other version.
//   - header names are validated against tchar; case folding is A-Z only.
//   - framing is headers + Content-Length, whatever the method and route:
//     `consumed` is computed here, so an early 401/400/404 can drain or close
//     without re-deriving it.
//   - the limits below are this reference's choices, to be aligned with the Go
//     codec's final constants.
//
// Refused (HTTP_REFUSE, the caller answers 400 with Connection: close and
// closes, whatever the handler wants): a bare CR or LF; NUL, CR or LF inside a
// value; obs-fold; whitespace before the colon; any Transfer-Encoding; a
// Content-Length that is non-numeric, empty, too long, or repeated; a repeated
// Authorization or Host.
#ifndef CREF_HTTP_PARSE_H
#define CREF_HTTP_PARSE_H

#include <stddef.h>
#include <stdint.h>

constexpr size_t HTTP_MAX_HEADER_BYTES = 8192;  // request line + headers + final CRLF
constexpr size_t HTTP_MAX_TARGET = 2048;
constexpr size_t HTTP_MAX_HEADERS = 64;
constexpr uint64_t HTTP_MAX_BODY = 1u << 20;
constexpr size_t HTTP_MAX_CL_DIGITS = 10;

enum http_status {
	HTTP_OK = 0,      // a full request, r->consumed bytes long
	HTTP_PARTIAL = 1, // more bytes needed; r->consumed is set once the headers are complete
	HTTP_REFUSE = 2,  // *why says which rule
};

enum http_refusal {
	HTTP_R_NONE = 0,
	HTTP_R_BAD_REQUEST_LINE,
	HTTP_R_BAD_METHOD,
	HTTP_R_BAD_TARGET,
	HTTP_R_BAD_VERSION,
	HTTP_R_BARE_CR,
	HTTP_R_BARE_LF,
	HTTP_R_NUL,
	HTTP_R_OBS_FOLD,
	HTTP_R_SPACE_BEFORE_COLON,
	HTTP_R_BAD_NAME,
	HTTP_R_TRANSFER_ENCODING,
	HTTP_R_BAD_CL,
	HTTP_R_DUP_CL,
	HTTP_R_DUP_AUTH,
	HTTP_R_DUP_HOST,
	HTTP_R_TOO_LARGE,
	HTTP_R_TOO_MANY_HEADERS,
	HTTP_R_BODY_TOO_LARGE,
};

struct http_slice {
	const char *p;
	size_t n;
};

struct http_header {
	struct http_slice name;
	struct http_slice value;
};

struct http_request {
	struct http_slice method;
	struct http_slice target;
	struct http_slice authorization;
	struct http_slice host;
	bool has_authorization;
	bool has_host;
	bool connection_close;
	uint64_t content_length;
	size_t header_len; // request line + headers, including the blank line
	size_t consumed;   // header_len + content_length
	size_t nheaders;
	struct http_header headers[HTTP_MAX_HEADERS];
};

enum http_status http_parse_request(const char *buf, size_t len, struct http_request *r,
				    enum http_refusal *why);

const char *http_refusal_name(enum http_refusal why);

#endif
