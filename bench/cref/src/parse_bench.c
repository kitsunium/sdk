// parse_bench - our S5-scope parser against picohttpparser on the same
// pipelined byte stream.
//
// usage: parse_bench own|pico [-n requests]
//        parse_bench stream    writes the pipelined byte stream to stdout, so the
//                              Go side can parse exactly the same bytes
//
// picohttpparser is informational: it implements no refusal of the S5 list, so
// it does strictly less checking. To make the two do the same job per request
// the pico side also finds Host, Authorization, Content-Length and
// Connection, as the application would, and computes `consumed`.
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "cref.h"
#include "http_parse.h"
#include "picohttpparser.h"

// A GET with a ~200-byte JWT, and a POST with a JSON body: the load's shapes.
static const char JWT[] =
	"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyMDAwMDQyIiwibmFtZSI6IkplYW4gRHVwb250Iiwi"
	"ZXhwIjoxODAwMDAwMDAwLCJpYXQiOjE3OTk5OTk5OTl9.dGhpc2lzbm90YXJlYWxzaWduYXR1cmVidXRsb29rc2xpa2Vp"
	"dA";

constexpr unsigned REPS = 32; // GET+POST pairs per pass: 64 pipelined requests
constexpr size_t STREAM_CAP = (size_t)64 * 1024;

static size_t build_stream(char *out, size_t cap, unsigned reps)
{
	size_t n = 0;
	for (unsigned i = 0; i < reps; i++) {
		int w = snprintf(out + n, cap - n,
				 "GET /users/%u HTTP/1.1\r\nHost: 10.0.0.7:3000\r\n"
				 "User-Agent: k6/1.0\r\nAccept: */*\r\nAccept-Encoding: gzip\r\n"
				 "Authorization: Bearer %s\r\nConnection: keep-alive\r\n\r\n",
				 i, JWT);
		if (w < 0 || (size_t)w >= cap - n)
			abort();
		n += (size_t)w;
		const char body[] = "{\"name\":\"Jean Dupont\",\"email\":\"jean@example.test\"}";
		w = snprintf(out + n, cap - n,
			     "POST /users HTTP/1.1\r\nHost: 10.0.0.7:3000\r\n"
			     "User-Agent: k6/1.0\r\nContent-Type: application/json\r\n"
			     "Authorization: Bearer %s\r\nContent-Length: %zu\r\nConnection: keep-alive\r\n\r\n%s",
			     JWT, sizeof body - 1, body);
		if (w < 0 || (size_t)w >= cap - n)
			abort();
		n += (size_t)w;
	}
	return n;
}

static bool ieq(const char *a, size_t an, const char *lit)
{
	size_t ln = strlen(lit);
	if (an != ln)
		return false;
	for (size_t i = 0; i < an; i++) {
		char c = a[i];
		if (c >= 'A' && c <= 'Z')
			c = (char)(c + 32);
		if (c != lit[i])
			return false;
	}
	return true;
}

// has_close_token scans a Connection value as a comma list, as the parser does.
static bool has_close_token(const char *v, size_t n)
{
	size_t i = 0;
	while (i < n) {
		while (i < n && (v[i] == ' ' || v[i] == '\t' || v[i] == ','))
			i++;
		size_t s = i;
		while (i < n && v[i] != ',' && v[i] != ' ' && v[i] != '\t')
			i++;
		if (ieq(v + s, i - s, "close"))
			return true;
	}
	return false;
}

static volatile uint64_t g_sink;

static uint64_t pass_own(const char *buf, size_t len)
{
	uint64_t acc = 0;
	size_t off = 0;
	struct http_request r;
	enum http_refusal why;

	while (off < len) {
		if (http_parse_request(buf + off, len - off, &r, &why) != HTTP_OK)
			abort();
		acc += r.consumed + r.authorization.n + r.host.n + r.content_length + (r.connection_close ? 1u : 0u);
		off += r.consumed;
		acc++;
	}
	return acc;
}

static uint64_t pass_pico(const char *buf, size_t len)
{
	uint64_t acc = 0;
	size_t off = 0;

	while (off < len) {
		const char *method, *path;
		size_t method_len, path_len, nh = 64;
		int minor;
		struct phr_header h[64];
		int hl = phr_parse_request(buf + off, len - off, &method, &method_len, &path, &path_len,
					   &minor, h, &nh, 0);
		if (hl < 0)
			abort();
		size_t cl = 0, auth = 0, host = 0, close_seen = 0;
		for (size_t i = 0; i < nh; i++) {
			if (ieq(h[i].name, h[i].name_len, "content-length")) {
				for (size_t k = 0; k < h[i].value_len; k++)
					cl = cl * 10 + (size_t)(h[i].value[k] - '0');
			} else if (ieq(h[i].name, h[i].name_len, "authorization")) {
				auth = h[i].value_len;
			} else if (ieq(h[i].name, h[i].name_len, "host")) {
				host = h[i].value_len;
			} else if (ieq(h[i].name, h[i].name_len, "connection")) {
				close_seen |= has_close_token(h[i].value, h[i].value_len) ? 1u : 0u;
			}
		}
		size_t consumed = (size_t)hl + cl;
		acc += consumed + auth + host + cl + close_seen;
		off += consumed;
		acc++;
	}
	return acc;
}

int main(int argc, char **argv)
{
	uint64_t want = 2000000;

	if (argc >= 2 && strcmp(argv[1], "stream") == 0) {
		char *s = malloc(STREAM_CAP);
		if (s == nullptr)
			return 1;
		size_t n = build_stream(s, STREAM_CAP, REPS);
		bool ok = fwrite(s, 1, n, stdout) == n;
		free(s);
		return ok ? 0 : 1;
	}
	if (argc < 2 || (strcmp(argv[1], "own") != 0 && strcmp(argv[1], "pico") != 0)) {
		fputs("usage: parse_bench own|pico [-n requests] | stream\n", stderr);
		return 2;
	}
	bool own = strcmp(argv[1], "own") == 0;
	if (argc == 4 && strcmp(argv[2], "-n") == 0)
		want = strtoull(argv[3], nullptr, 10);

	char *buf = malloc(STREAM_CAP);
	if (buf == nullptr)
		return 1;
	size_t len = build_stream(buf, STREAM_CAP, REPS);
	uint64_t per_pass = 2u * (uint64_t)REPS;
	uint64_t passes = want / per_pass + 1;

	// Both parsers must agree on the work done before it is timed.
	if (pass_own(buf, len) != pass_pico(buf, len)) {
		fputs("own and pico disagree on the stream\n", stderr);
		return 1;
	}
	uint64_t acc = 0;
	for (uint64_t i = 0; i < passes / 10 + 1; i++)
		acc += own ? pass_own(buf, len) : pass_pico(buf, len);

	uint64_t t0 = cref_now_ns();
	struct cref_cpu c0 = cref_cpu_now();
	for (uint64_t i = 0; i < passes; i++)
		acc += own ? pass_own(buf, len) : pass_pico(buf, len);
	uint64_t t1 = cref_now_ns();
	struct cref_cpu c1 = cref_cpu_now();

	g_sink = acc;
	cref_report(own ? "parse_own" : "parse_pico", passes * per_pass, t1 - t0, c0, c1);
	free(buf);
	return 0;
}
