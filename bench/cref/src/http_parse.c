#include "http_parse.h"

#include <string.h>

// Byte classes. TCHAR is RFC 9110 tchar; CTL covers the C0 controls and DEL.
enum : uint8_t { C_TCHAR = 1, C_CTL = 2 };

#define IS_TCHAR(c)                                                                       \
	(((c) >= '0' && (c) <= '9') || ((c) >= 'A' && (c) <= 'Z') || ((c) >= 'a' && (c) <= 'z') || \
	 (c) == '!' || (c) == '#' || (c) == '$' || (c) == '%' || (c) == '&' || (c) == '\'' ||    \
	 (c) == '*' || (c) == '+' || (c) == '-' || (c) == '.' || (c) == '^' || (c) == '_' ||      \
	 (c) == '`' || (c) == '|' || (c) == '~')
#define class_of(c) ((uint8_t)((IS_TCHAR(c) ? C_TCHAR : 0) | (((c) < 0x20 || (c) == 0x7f) ? C_CTL : 0)))

#define R4(n) class_of(n), class_of((n) + 1), class_of((n) + 2), class_of((n) + 3)
#define R16(n) R4(n), R4((n) + 4), R4((n) + 8), R4((n) + 12)
#define R64(n) R16(n), R16((n) + 16), R16((n) + 32), R16((n) + 48)
static const uint8_t CLASS[256] = {R64(0), R64(64), R64(128), R64(192)};

// SWAR scans: eight bytes per step, used only as a yes/no "does any byte of
// this word need a closer look". Both tests are exact as booleans (the usual
// haszero/hasless bit tricks; hasless is valid for n <= 128), and a hit falls
// back to the byte loop, so the result does not depend on byte order.
#define ONES 0x0101010101010101ull
#define HIGHS 0x8080808080808080ull

static inline uint64_t load64(const char *p)
{
	uint64_t v;
	memcpy(&v, p, sizeof v);
	return v;
}
static inline uint64_t has_zero(uint64_t v) { return (v - ONES) & ~v & HIGHS; }
static inline uint64_t has_less(uint64_t v, unsigned n) { return (v - ONES * n) & ~v & HIGHS; }

// Case folding is A-Z only: no locale, no Unicode, no Kelvin sign.
static inline unsigned fold(unsigned char c)
{
	return (c >= 'A' && c <= 'Z') ? c + 32u : c;
}

static bool name_is(const char *p, size_t n, const char *lit, size_t lit_n)
{
	if (n != lit_n)
		return false;
	for (size_t i = 0; i < n; i++)
		if (fold((unsigned char)p[i]) != (unsigned char)lit[i])
			return false;
	return true;
}
#define NAME_IS(p, n, lit) name_is((p), (n), lit, sizeof(lit) - 1)

static enum http_status refuse(enum http_refusal *why, enum http_refusal reason)
{
	*why = reason;
	return HTTP_REFUSE;
}

// ran_out reports that the scan reached the end of the available bytes: the
// request is incomplete, unless the header budget is already spent.
static enum http_status ran_out(size_t len, enum http_refusal *why)
{
	if (len >= HTTP_MAX_HEADER_BYTES)
		return refuse(why, HTTP_R_TOO_LARGE);
	return HTTP_PARTIAL;
}

// connection_has_close scans a Connection value as a comma list of tokens.
static bool connection_has_close(const char *p, size_t n)
{
	size_t i = 0;
	while (i < n) {
		while (i < n && (p[i] == ' ' || p[i] == '\t' || p[i] == ','))
			i++;
		size_t s = i;
		while (i < n && p[i] != ',' && p[i] != ' ' && p[i] != '\t')
			i++;
		if (NAME_IS(p + s, i - s, "close"))
			return true;
	}
	return false;
}

static enum http_status parse_content_length(const char *p, size_t n, struct http_request *r,
					     enum http_refusal *why)
{
	if (n == 0 || n > HTTP_MAX_CL_DIGITS)
		return refuse(why, HTTP_R_BAD_CL);
	uint64_t v = 0;
	for (size_t i = 0; i < n; i++) {
		unsigned d = (unsigned char)p[i] - (unsigned)'0';
		if (d > 9)
			return refuse(why, HTTP_R_BAD_CL);
		v = v * 10 + d;
	}
	if (v > HTTP_MAX_BODY)
		return refuse(why, HTTP_R_BODY_TOO_LARGE);
	r->content_length = v;
	return HTTP_OK;
}

enum http_status http_parse_request(const char *buf, size_t len, struct http_request *r,
				    enum http_refusal *why)
{
	const size_t limit = len < HTTP_MAX_HEADER_BYTES ? len : HTTP_MAX_HEADER_BYTES;
	size_t i = 0;
	bool have_cl = false;

	*why = HTTP_R_NONE;
	r->has_authorization = r->has_host = r->connection_close = false;
	r->method = r->target = r->authorization = r->host = (struct http_slice){nullptr, 0};
	r->content_length = 0;
	r->header_len = r->consumed = r->nheaders = 0;

	// Request line: method.
	size_t s = i;
	while (i < limit && (CLASS[(unsigned char)buf[i]] & C_TCHAR))
		i++;
	if (i == limit)
		return ran_out(len, why);
	if (i == s)
		return refuse(why, HTTP_R_BAD_METHOD);
	if (buf[i] != ' ')
		return refuse(why, buf[i] == '\r' ? HTTP_R_BARE_CR
				   : buf[i] == '\n' ? HTTP_R_BARE_LF
						    : HTTP_R_BAD_METHOD);
	r->method = (struct http_slice){buf + s, i - s};
	i++;

	// Target: no space, no control byte.
	s = i;
	while (i + 8 <= limit) { // skip words that hold no space, control byte or DEL
		uint64_t x = load64(buf + i);
		if (has_less(x, 0x21) | has_zero(x ^ (ONES * 0x7f)))
			break;
		i += 8;
	}
	while (i < limit && buf[i] != ' ' && !(CLASS[(unsigned char)buf[i]] & C_CTL))
		i++;
	if (i == limit)
		return ran_out(len, why);
	if (i == s || i - s > HTTP_MAX_TARGET)
		return refuse(why, i == s ? HTTP_R_BAD_TARGET : HTTP_R_TOO_LARGE);
	if (buf[i] != ' ')
		return refuse(why, buf[i] == '\r' ? HTTP_R_BARE_CR
				   : buf[i] == '\n' ? HTTP_R_BARE_LF
						    : HTTP_R_BAD_TARGET);
	r->target = (struct http_slice){buf + s, i - s};
	i++;

	// Version, then CRLF.
	static const char VERSION[] = "HTTP/1.1";
	constexpr size_t VN = sizeof VERSION - 1;
	if (limit - i < VN + 2) {
		// Refuse early on a prefix that can no longer match.
		size_t avail = limit - i;
		if (memcmp(buf + i, VERSION, avail < VN ? avail : VN) != 0)
			return refuse(why, HTTP_R_BAD_VERSION);
		return ran_out(len, why);
	}
	if (memcmp(buf + i, VERSION, VN) != 0)
		return refuse(why, HTTP_R_BAD_VERSION);
	i += VN;
	if (buf[i] == '\n')
		return refuse(why, HTTP_R_BARE_LF);
	if (buf[i] != '\r')
		return refuse(why, HTTP_R_BAD_REQUEST_LINE);
	if (buf[i + 1] != '\n')
		return refuse(why, HTTP_R_BARE_CR);
	i += 2;

	// Header lines until the blank line.
	for (;;) {
		if (i == limit)
			return ran_out(len, why);
		unsigned char c = (unsigned char)buf[i];
		if (c == '\r') {
			if (i + 1 == limit)
				return ran_out(len, why);
			if (buf[i + 1] != '\n')
				return refuse(why, HTTP_R_BARE_CR);
			i += 2;
			break;
		}
		if (c == ' ' || c == '\t')
			return refuse(why, HTTP_R_OBS_FOLD);
		if (c == '\n')
			return refuse(why, HTTP_R_BARE_LF);
		if (r->nheaders == HTTP_MAX_HEADERS)
			return refuse(why, HTTP_R_TOO_MANY_HEADERS);

		s = i;
		while (i < limit && (CLASS[(unsigned char)buf[i]] & C_TCHAR))
			i++;
		if (i == limit)
			return ran_out(len, why);
		size_t name_n = i - s;
		if (name_n == 0 || buf[i] != ':') {
			unsigned char b = (unsigned char)buf[i];
			if (b == '\r' && i + 1 == limit)
				return ran_out(len, why);
			if (b == '\r' && buf[i + 1] == '\n')
				return refuse(why, HTTP_R_BAD_NAME); // a header line with no colon
			return refuse(why, (b == ' ' || b == '\t') && name_n > 0 ? HTTP_R_SPACE_BEFORE_COLON
				      : b == '\r'                                   ? HTTP_R_BARE_CR
				      : b == '\n'                                   ? HTTP_R_BARE_LF
				      : b == 0                                      ? HTTP_R_NUL
										    : HTTP_R_BAD_NAME);
		}
		const char *name_p = buf + s;
		i++; // the colon

		while (i < limit && (buf[i] == ' ' || buf[i] == '\t'))
			i++;
		size_t vs = i;
		while (i + 8 <= limit) { // skip words that hold no CR, LF or NUL
			uint64_t x = load64(buf + i);
			if (has_zero(x) | has_zero(x ^ (ONES * 0x0d)) | has_zero(x ^ (ONES * 0x0a)))
				break;
			i += 8;
		}
		while (i < limit) {
			unsigned char b = (unsigned char)buf[i];
			if (b == '\r')
				break;
			if (b == '\n')
				return refuse(why, HTTP_R_BARE_LF);
			if (b == 0)
				return refuse(why, HTTP_R_NUL);
			i++;
		}
		if (i == limit || i + 1 == limit)
			return ran_out(len, why);
		if (buf[i + 1] != '\n')
			return refuse(why, HTTP_R_BARE_CR);
		size_t ve = i;
		while (ve > vs && (buf[ve - 1] == ' ' || buf[ve - 1] == '\t'))
			ve--;
		i += 2;

		const char *val_p = buf + vs;
		size_t val_n = ve - vs;
		switch (name_n) {
		case 4:
			if (NAME_IS(name_p, name_n, "host")) {
				if (r->has_host)
					return refuse(why, HTTP_R_DUP_HOST);
				r->has_host = true;
				r->host = (struct http_slice){val_p, val_n};
			}
			break;
		case 10:
			if (NAME_IS(name_p, name_n, "connection") && connection_has_close(val_p, val_n))
				r->connection_close = true;
			break;
		case 13:
			if (NAME_IS(name_p, name_n, "authorization")) {
				if (r->has_authorization)
					return refuse(why, HTTP_R_DUP_AUTH);
				r->has_authorization = true;
				r->authorization = (struct http_slice){val_p, val_n};
			}
			break;
		case 14:
			if (NAME_IS(name_p, name_n, "content-length")) {
				if (have_cl)
					return refuse(why, HTTP_R_DUP_CL);
				have_cl = true;
				if (parse_content_length(val_p, val_n, r, why) != HTTP_OK)
					return HTTP_REFUSE;
			}
			break;
		case 17:
			if (NAME_IS(name_p, name_n, "transfer-encoding"))
				return refuse(why, HTTP_R_TRANSFER_ENCODING);
			break;
		default:
			break;
		}
		r->headers[r->nheaders++] = (struct http_header){{name_p, name_n}, {val_p, val_n}};
	}

	r->header_len = i;
	r->consumed = i + (size_t)r->content_length;
	if (len < r->consumed)
		return HTTP_PARTIAL;
	return HTTP_OK;
}

const char *http_refusal_name(enum http_refusal why)
{
	switch (why) {
	case HTTP_R_NONE: return "none";
	case HTTP_R_BAD_REQUEST_LINE: return "bad_request_line";
	case HTTP_R_BAD_METHOD: return "bad_method";
	case HTTP_R_BAD_TARGET: return "bad_target";
	case HTTP_R_BAD_VERSION: return "bad_version";
	case HTTP_R_BARE_CR: return "bare_cr";
	case HTTP_R_BARE_LF: return "bare_lf";
	case HTTP_R_NUL: return "nul";
	case HTTP_R_OBS_FOLD: return "obs_fold";
	case HTTP_R_SPACE_BEFORE_COLON: return "space_before_colon";
	case HTTP_R_BAD_NAME: return "bad_name";
	case HTTP_R_TRANSFER_ENCODING: return "transfer_encoding";
	case HTTP_R_BAD_CL: return "bad_content_length";
	case HTTP_R_DUP_CL: return "dup_content_length";
	case HTTP_R_DUP_AUTH: return "dup_authorization";
	case HTTP_R_DUP_HOST: return "dup_host";
	case HTTP_R_TOO_LARGE: return "too_large";
	case HTTP_R_TOO_MANY_HEADERS: return "too_many_headers";
	case HTTP_R_BODY_TOO_LARGE: return "body_too_large";
	}
	return "unknown";
}
