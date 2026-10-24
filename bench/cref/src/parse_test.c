// parse_test - the corpus for http_parse.c: accepted requests with their
// framing, every refusal, and the smuggling vectors. Exit 0 only if every case
// holds. Run under ASan/UBSan by `make sanitize`.
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "http_parse.h"

static int failures;

#define CHECK(cond, ...)                                                          \
	do {                                                                      \
		if (!(cond)) {                                                    \
			failures++;                                               \
			fprintf(stderr, "FAIL %s:%d: ", __FILE__, __LINE__);     \
			fprintf(stderr, __VA_ARGS__);                             \
			fputc('\n', stderr);                                      \
		}                                                                 \
	} while (0)

struct refusal_case {
	const char *name;
	const char *in;
	size_t n; // 0 means strlen(in); set it when the input holds a NUL
	enum http_refusal want;
};

static void expect_refusal(const struct refusal_case *c)
{
	struct http_request r;
	enum http_refusal why;
	size_t n = c->n ? c->n : strlen(c->in);
	enum http_status st = http_parse_request(c->in, n, &r, &why);

	CHECK(st == HTTP_REFUSE && why == c->want, "%s: status %d reason %s, want refusal %s", c->name,
	      st, http_refusal_name(why), http_refusal_name(c->want));
}

static void test_refusals(void)
{
	static const struct refusal_case cases[] = {
		{"bare LF line endings", "GET / HTTP/1.1\nHost: a\n\n", 0, HTTP_R_BARE_LF},
		{"bare LF after a header", "GET / HTTP/1.1\r\nHost: a\n\r\n", 0, HTTP_R_BARE_LF},
		{"bare CR after request line", "GET / HTTP/1.1\rHost: a\r\n\r\n", 0, HTTP_R_BARE_CR},
		{"bare CR in a value", "GET / HTTP/1.1\r\nX: a\rb\r\n\r\n", 0, HTTP_R_BARE_CR},
		{"bare CR ends the headers", "GET / HTTP/1.1\r\n\rX", 0, HTTP_R_BARE_CR},
		{"NUL in a value", "GET / HTTP/1.1\r\nX: a\0b\r\n\r\n", 26, HTTP_R_NUL},
		{"obs-fold with space", "GET / HTTP/1.1\r\nX: a\r\n b\r\n\r\n", 0, HTTP_R_OBS_FOLD},
		{"obs-fold with tab", "GET / HTTP/1.1\r\nX: a\r\n\tb\r\n\r\n", 0, HTTP_R_OBS_FOLD},
		{"space before colon", "GET / HTTP/1.1\r\nHost : a\r\n\r\n", 0, HTTP_R_SPACE_BEFORE_COLON},
		{"tab before colon", "GET / HTTP/1.1\r\nHost\t: a\r\n\r\n", 0, HTTP_R_SPACE_BEFORE_COLON},
		{"TE chunked", "POST / HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n", 0, HTTP_R_TRANSFER_ENCODING},
		{"TE any value, any case", "POST / HTTP/1.1\r\ntRaNsFeR-eNcOdInG: identity\r\n\r\n", 0, HTTP_R_TRANSFER_ENCODING},
		{"CL and TE", "POST / HTTP/1.1\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\nabcd", 0, HTTP_R_TRANSFER_ENCODING},
		{"TE with space before colon", "POST / HTTP/1.1\r\nTransfer-Encoding : chunked\r\n\r\n", 0, HTTP_R_SPACE_BEFORE_COLON},
		{"CL empty", "POST / HTTP/1.1\r\nContent-Length:\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL signed", "POST / HTTP/1.1\r\nContent-Length: +5\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL negative", "POST / HTTP/1.1\r\nContent-Length: -1\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL hex", "POST / HTTP/1.1\r\nContent-Length: 0x10\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL inner space", "POST / HTTP/1.1\r\nContent-Length: 5 5\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL list", "POST / HTTP/1.1\r\nContent-Length: 5, 5\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL too long", "POST / HTTP/1.1\r\nContent-Length: 00000000001\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL overflow", "POST / HTTP/1.1\r\nContent-Length: 99999999999999999999\r\n\r\n", 0, HTTP_R_BAD_CL},
		{"CL over the body cap", "POST / HTTP/1.1\r\nContent-Length: 1048577\r\n\r\n", 0, HTTP_R_BODY_TOO_LARGE},
		{"CL repeated, divergent", "POST / HTTP/1.1\r\nContent-Length: 4\r\nContent-Length: 5\r\n\r\n", 0, HTTP_R_DUP_CL},
		{"CL repeated, identical", "POST / HTTP/1.1\r\nContent-Length: 4\r\ncontent-length: 4\r\n\r\n", 0, HTTP_R_DUP_CL},
		{"Authorization repeated", "GET / HTTP/1.1\r\nAuthorization: a\r\nauthorization: b\r\n\r\n", 0, HTTP_R_DUP_AUTH},
		{"Host repeated", "GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n", 0, HTTP_R_DUP_HOST},
		{"name with non-tchar", "GET / HTTP/1.1\r\nX(y): a\r\n\r\n", 0, HTTP_R_BAD_NAME},
		{"empty name", "GET / HTTP/1.1\r\n: a\r\n\r\n", 0, HTTP_R_BAD_NAME},
		{"Kelvin sign is not K", "GET / HTTP/1.1\r\nContent-Length: 4\r\n\xe2\x84\xaa: x\r\n\r\n", 0, HTTP_R_BAD_NAME},
		{"line without colon", "GET / HTTP/1.1\r\nHost\r\n\r\n", 0, HTTP_R_BAD_NAME},
		{"HTTP/1.0", "GET / HTTP/1.0\r\n\r\n", 0, HTTP_R_BAD_VERSION},
		{"HTTP/2", "GET / HTTP/2.0\r\n\r\n", 0, HTTP_R_BAD_VERSION},
		{"lowercase version", "GET / http/1.1\r\n\r\n", 0, HTTP_R_BAD_VERSION},
		{"empty method", " / HTTP/1.1\r\n\r\n", 0, HTTP_R_BAD_METHOD},
		{"method with a separator", "G(T / HTTP/1.1\r\n\r\n", 0, HTTP_R_BAD_METHOD},
		{"empty target", "GET  HTTP/1.1\r\n\r\n", 0, HTTP_R_BAD_TARGET},
		{"control byte in target", "GET /a\x01 HTTP/1.1\r\n\r\n", 0, HTTP_R_BAD_TARGET},
		{"junk after version", "GET / HTTP/1.1 x\r\n\r\n", 0, HTTP_R_BAD_REQUEST_LINE},
	};

	for (size_t i = 0; i < sizeof cases / sizeof cases[0]; i++)
		expect_refusal(&cases[i]);
}

static void test_limits(void)
{
	struct http_request r;
	enum http_refusal why;
	char *buf = malloc(HTTP_MAX_HEADER_BYTES + 64);
	size_t n;

	if (buf == nullptr)
		abort();

	// Header block exactly at the budget is accepted, one byte over is not.
	const char head[] = "GET / HTTP/1.1\r\nX: ";
	const char tail[] = "\r\n\r\n";
	for (int over = 0; over <= 1; over++) {
		size_t total = HTTP_MAX_HEADER_BYTES + (size_t)over;
		n = 0;
		memcpy(buf, head, sizeof head - 1);
		n += sizeof head - 1;
		memset(buf + n, 'v', total - (sizeof head - 1) - (sizeof tail - 1));
		n = total - (sizeof tail - 1);
		memcpy(buf + n, tail, sizeof tail - 1);
		n += sizeof tail - 1;
		enum http_status st = http_parse_request(buf, n, &r, &why);
		if (over == 0)
			CHECK(st == HTTP_OK, "header block at the budget: status %d (%s)", st,
			      http_refusal_name(why));
		else
			CHECK(st == HTTP_REFUSE && why == HTTP_R_TOO_LARGE,
			      "header block over the budget: status %d (%s)", st, http_refusal_name(why));
	}

	// A header block that never ends is refused once the budget is spent.
	memset(buf, 'a', HTTP_MAX_HEADER_BYTES + 64);
	memcpy(buf, "GET /", 5);
	CHECK(http_parse_request(buf, HTTP_MAX_HEADER_BYTES + 64, &r, &why) == HTTP_REFUSE &&
		      why == HTTP_R_TOO_LARGE,
	      "endless target: %s", http_refusal_name(why));

	// Too many headers.
	memcpy(buf, "GET / HTTP/1.1\r\n", 16);
	n = 16;
	for (size_t i = 0; i < HTTP_MAX_HEADERS + 1; i++)
		n += (size_t)snprintf(buf + n, 32, "X%zu: v\r\n", i);
	memcpy(buf + n, "\r\n", 2);
	n += 2;
	CHECK(http_parse_request(buf, n, &r, &why) == HTTP_REFUSE && why == HTTP_R_TOO_MANY_HEADERS,
	      "too many headers: %s", http_refusal_name(why));
	free(buf);
}

static void test_accepts(void)
{
	struct http_request r;
	enum http_refusal why;

	{ // plain GET, case-folded names, OWS trimmed, Connection token list
		const char in[] = "GET /users/42 HTTP/1.1\r\nhOsT:  example.test \t\r\n"
				  "AUTHORIZATION:\tBearer abc.def.ghi\r\n"
				  "Connection: keep-alive, Close\r\nAccept: */*\r\n\r\n";
		enum http_status st = http_parse_request(in, sizeof in - 1, &r, &why);
		CHECK(st == HTTP_OK, "plain GET: %d %s", st, http_refusal_name(why));
		CHECK(r.consumed == sizeof in - 1 && r.header_len == r.consumed, "plain GET framing");
		CHECK(r.method.n == 3 && memcmp(r.method.p, "GET", 3) == 0, "method");
		CHECK(r.target.n == 9 && memcmp(r.target.p, "/users/42", 9) == 0, "target");
		CHECK(r.has_host && r.host.n == 12 && memcmp(r.host.p, "example.test", 12) == 0, "host");
		CHECK(r.has_authorization && r.authorization.n == 18 &&
			      memcmp(r.authorization.p, "Bearer abc.def.ghi", 18) == 0,
		      "authorization");
		CHECK(r.connection_close, "Connection: close token");
		CHECK(r.nheaders == 4, "header count %zu", r.nheaders);
	}
	{ // leading zeros and a zero body
		const char in[] = "POST /x HTTP/1.1\r\nContent-Length: 0007\r\n\r\n1234567";
		enum http_status st = http_parse_request(in, sizeof in - 1, &r, &why);
		CHECK(st == HTTP_OK && r.content_length == 7 && r.consumed == sizeof in - 1,
		      "leading zeros: %d, cl %llu", st, (unsigned long long)r.content_length);
	}
	{ // a body not yet complete: PARTIAL, with the total already known
		const char in[] = "POST /x HTTP/1.1\r\nContent-Length: 10\r\n\r\n12345";
		enum http_status st = http_parse_request(in, sizeof in - 1, &r, &why);
		CHECK(st == HTTP_PARTIAL && r.consumed == r.header_len + 10, "partial body: %d", st);
	}
	{ // Connection header that is not close
		const char in[] = "GET / HTTP/1.1\r\nConnection: keep-alive\r\n\r\n";
		CHECK(http_parse_request(in, sizeof in - 1, &r, &why) == HTTP_OK && !r.connection_close,
		      "keep-alive is not close");
	}
	{ // a value may carry bytes >= 0x80 and a colon
		const char in[] = "GET / HTTP/1.1\r\nX-A: a:b\xc3\xa9\r\n\r\n";
		CHECK(http_parse_request(in, sizeof in - 1, &r, &why) == HTTP_OK, "obs-text value");
	}
}

// Every strict prefix of a valid request must be PARTIAL, and only the whole
// thing OK: the parser never refuses what more bytes would make valid, and
// never accepts early.
static void test_prefixes(void)
{
	static const char *const reqs[] = {
		"GET /users/1 HTTP/1.1\r\nHost: h\r\nAuthorization: Bearer x\r\n\r\n",
		"POST /users HTTP/1.1\r\nHost: h\r\nContent-Length: 12\r\n\r\n{\"a\":\"bcde\"}",
	};
	struct http_request r;
	enum http_refusal why;

	for (size_t k = 0; k < sizeof reqs / sizeof reqs[0]; k++) {
		size_t full = strlen(reqs[k]);
		for (size_t n = 0; n <= full; n++) {
			enum http_status st = http_parse_request(reqs[k], n, &r, &why);
			enum http_status want = n == full ? HTTP_OK : HTTP_PARTIAL;
			CHECK(st == want, "request %zu prefix %zu/%zu: status %d (%s)", k, n, full, st,
			      http_refusal_name(why));
		}
	}
}

// Framing does not depend on method or route. A GET whose Content-Length
// wraps a whole second request consumes it as body; the parser must not hand
// the smuggled request back. The same for a POST without auth and for a 404
// route: consumed is headers + CL every time.
static void test_framing(void)
{
	struct http_request r;
	enum http_refusal why;
	const char inner[] = "GET /admin HTTP/1.1\r\nHost: h\r\n\r\n";
	char buf[512];

	static const char *const methods[] = {"GET", "POST", "DELETE", "PUT"};
	static const char *const routes[] = {"/users/1", "/no/such/route", "/"};
	for (size_t m = 0; m < 4; m++)
		for (size_t t = 0; t < 3; t++) {
			int n = snprintf(buf, sizeof buf,
					 "%s %s HTTP/1.1\r\nHost: h\r\nContent-Length: %zu\r\n\r\n%s%s",
					 methods[m], routes[t], sizeof inner - 1, inner, "GET /next HTTP/1.1\r\n\r\n");
			CHECK(n > 0, "snprintf");
			enum http_status st = http_parse_request(buf, (size_t)n, &r, &why);
			CHECK(st == HTTP_OK, "%s %s: %d", methods[m], routes[t], st);
			CHECK(r.consumed == r.header_len + sizeof inner - 1,
			      "%s %s: consumed %zu header %zu", methods[m], routes[t], r.consumed, r.header_len);

			// The next request starts after the body, not inside it.
			const char *next = buf + r.consumed;
			size_t next_len = (size_t)n - r.consumed;
			CHECK(http_parse_request(next, next_len, &r, &why) == HTTP_OK && r.target.n == 5 &&
				      memcmp(r.target.p, "/next", 5) == 0,
			      "%s %s: pipelined request after the body", methods[m], routes[t]);
		}
}


// Deterministic mutation fuzz. Every input lives in a heap block of exactly its
// length, so under ASan a read past `len` (the SWAR words included) is a
// finding. Invariants on whatever the parser answers: slices lie inside the
// input, header_len <= consumed, an OK request fits in len; and the answer does
// not change when the same bytes sit in a larger buffer whose slack is filled
// with "\r\n\r\n" (the parser must never look past len).
static uint64_t fuzz_state = 0x2545f4914f6cdd1dull;

static uint64_t fuzz_next(void)
{
	fuzz_state ^= fuzz_state >> 12;
	fuzz_state ^= fuzz_state << 25;
	fuzz_state ^= fuzz_state >> 27;
	return fuzz_state * 0x2545f4914f6cdd1dull;
}

static bool inside(const char *base, size_t len, struct http_slice sl)
{
	return sl.n == 0 || (sl.p >= base && sl.p + sl.n <= base + len);
}

static void test_fuzz(void)
{
	static const char *const seeds[] = {
		"GET /users/42 HTTP/1.1\r\nHost: example.test\r\nAuthorization: Bearer abc.def.ghi\r\n"
		"Connection: keep-alive\r\nAccept: */*\r\n\r\n",
		"POST /users HTTP/1.1\r\nHost: h\r\nContent-Length: 12\r\nX-Long: "
		"0123456789abcdef0123456789abcdef0123456789\r\n\r\n{\"a\":\"bcde\"}",
	};
	static const char interesting[] = {'\r', '\n', '\0', ' ', '\t', ':', 'a', '0', 0x7f, (char)0x80, (char)0xff};

	for (int iter = 0; iter < 200000; iter++) {
		const char *seed = seeds[fuzz_next() % 2];
		size_t slen = strlen(seed);
		size_t len = slen;
		char tmp[512];

		memcpy(tmp, seed, slen);
		unsigned edits = 1 + (unsigned)(fuzz_next() % 3);
		for (unsigned e = 0; e < edits; e++) {
			size_t at = (size_t)(fuzz_next() % len);
			char c = interesting[fuzz_next() % sizeof interesting];
			switch (fuzz_next() % 3) {
			case 0: tmp[at] = c; break;
			case 1:
				if (len < sizeof tmp - 1) {
					memmove(tmp + at + 1, tmp + at, len - at);
					tmp[at] = c;
					len++;
				}
				break;
			default:
				if (len > 1) {
					memmove(tmp + at, tmp + at + 1, len - at - 1);
					len--;
				}
				break;
			}
		}
		if (fuzz_next() % 4 == 0)
			len = (size_t)(fuzz_next() % (len + 1)); // truncate

		char *exact = malloc(len ? len : 1);
		char *roomy = malloc(len + 16);
		if (exact == nullptr || roomy == nullptr)
			abort();
		memcpy(exact, tmp, len);
		memcpy(roomy, tmp, len);
		memcpy(roomy + len, "\r\n\r\n\r\n\r\n\r\n\r\n\r\n\r\n", 16);

		struct http_request a, b;
		enum http_refusal wa, wb;
		enum http_status sa = http_parse_request(exact, len, &a, &wa);
		enum http_status sb = http_parse_request(roomy, len, &b, &wb);

		CHECK(sa == sb && wa == wb, "iter %d: answer depends on bytes past len (%d/%s vs %d/%s)", iter,
		      sa, http_refusal_name(wa), sb, http_refusal_name(wb));
		if (sa == HTTP_OK)
			CHECK(a.consumed <= len && a.header_len <= a.consumed, "iter %d: OK but consumed %zu header %zu len %zu",
			      iter, a.consumed, a.header_len, len);
		if (sa != HTTP_REFUSE) {
			CHECK(inside(exact, len, a.method) && inside(exact, len, a.target) &&
				      inside(exact, len, a.authorization) && inside(exact, len, a.host),
			      "iter %d: slice outside the input", iter);
			for (size_t h = 0; h < a.nheaders; h++)
				CHECK(inside(exact, len, a.headers[h].name) && inside(exact, len, a.headers[h].value),
				      "iter %d: header %zu outside the input", iter, h);
		}
		free(exact);
		free(roomy);
		if (failures > 20)
			return;
	}
}

int main(void)
{
	test_refusals();
	test_limits();
	test_accepts();
	test_prefixes();
	test_framing();
	test_fuzz();
	if (failures != 0) {
		fprintf(stderr, "parse_test: %d failure(s)\n", failures);
		return 1;
	}
	puts("parse_test=ok");
	return 0;
}
