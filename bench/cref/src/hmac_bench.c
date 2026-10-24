// hmac_bench - HMAC-SHA256 over a JWT-sized signing input, in the two forms
// the C reference keeps. Whichever is faster is the number the Go side is
// compared with.
//
//   evp : OpenSSL 3 EVP_MAC fetched once, one keyed template EVP_MAC_CTX, and
//         per message EVP_MAC_CTX_dup + update + final.
//   reinit : one keyed EVP_MAC_CTX reused; per message EVP_MAC_init(ctx, NULL, 0,
//         NULL) resets it to the keyed state, then update + final.
//   mid : the ipad and opad blocks are hashed once into two SHA-256
//         midstates; per message a struct copy, update, final, twice.
//
// usage: hmac_bench selftest
//        hmac_bench evp|reinit|mid [-n iterations] [-l message_length]
//
// The midstate form uses the low-level SHA256_* API, deprecated since
// OpenSSL 3.0 but still shipped: a midstate cannot be copied through EVP
// without EVP_MD_CTX_copy_ex's allocation, which is exactly the cost this form
// exists to remove.
#define OPENSSL_SUPPRESS_DEPRECATED

#include <openssl/core_names.h>
#include <openssl/crypto.h>
#include <openssl/evp.h>
#include <openssl/params.h>
#include <openssl/sha.h>

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "cref.h"

constexpr size_t KEY_LEN = 32;
constexpr size_t MAC_LEN = 32;
constexpr size_t BLOCK = 64;
constexpr size_t MAX_MSG = 4096;

static volatile uint8_t g_sink; // keeps the MAC output observable

struct evp_state {
	EVP_MAC *mac;
	EVP_MAC_CTX *tmpl;
};

struct mid_state {
	SHA256_CTX in;
	SHA256_CTX out;
};

static void evp_free(struct evp_state *st);

// evp_init leaves st releasable with evp_free whatever it returns.
static int evp_init(struct evp_state *st, const uint8_t *key, size_t key_len)
{
	char digest[] = "SHA256";
	OSSL_PARAM params[2] = {
		OSSL_PARAM_construct_utf8_string(OSSL_MAC_PARAM_DIGEST, digest, 0),
		OSSL_PARAM_construct_end(),
	};

	st->mac = nullptr;
	st->tmpl = nullptr;
	st->mac = EVP_MAC_fetch(nullptr, "HMAC", nullptr);
	if (st->mac == nullptr)
		return -1;
	st->tmpl = EVP_MAC_CTX_new(st->mac);
	if (st->tmpl == nullptr || EVP_MAC_init(st->tmpl, key, key_len, params) != 1)
		return -1;
	return 0;
}

static void evp_free(struct evp_state *st)
{
	EVP_MAC_CTX_free(st->tmpl);
	EVP_MAC_free(st->mac);
	st->tmpl = nullptr;
	st->mac = nullptr;
}

// reinit_mac reuses the keyed template itself: EVP_MAC_init with no key resets
// the context to the keyed state, with no allocation. The state is mutated, so
// this form is single-threaded by construction, like the rest of the bench.
static int reinit_mac(const struct evp_state *st, const uint8_t *msg, size_t len, uint8_t out[MAC_LEN])
{
	size_t outl = 0;

	return EVP_MAC_init(st->tmpl, nullptr, 0, nullptr) == 1 && EVP_MAC_update(st->tmpl, msg, len) == 1 &&
			       EVP_MAC_final(st->tmpl, out, &outl, MAC_LEN) == 1 && outl == MAC_LEN
		       ? 0
		       : -1;
}

static int evp_mac(const struct evp_state *st, const uint8_t *msg, size_t len, uint8_t out[MAC_LEN])
{
	size_t outl = 0;
	EVP_MAC_CTX *c = EVP_MAC_CTX_dup(st->tmpl);

	if (c == nullptr)
		return -1;
	int ok = EVP_MAC_update(c, msg, len) == 1 && EVP_MAC_final(c, out, &outl, MAC_LEN) == 1 &&
		 outl == MAC_LEN;
	EVP_MAC_CTX_free(c);
	return ok ? 0 : -1;
}

static int mid_init(struct mid_state *st, const uint8_t *key, size_t key_len)
{
	uint8_t ipad[BLOCK] = {0};
	uint8_t opad[BLOCK] = {0};
	int rc = -1;

	if (key_len > BLOCK)
		goto out; // a key over one block would be hashed first; the SDK never has one
	memcpy(ipad, key, key_len);
	memcpy(opad, key, key_len);
	for (size_t i = 0; i < BLOCK; i++) {
		ipad[i] ^= 0x36u;
		opad[i] ^= 0x5cu;
	}
	if (SHA256_Init(&st->in) == 1 && SHA256_Update(&st->in, ipad, BLOCK) == 1 && SHA256_Init(&st->out) == 1 &&
	    SHA256_Update(&st->out, opad, BLOCK) == 1)
		rc = 0;
out:
	OPENSSL_cleanse(ipad, sizeof ipad);
	OPENSSL_cleanse(opad, sizeof opad);
	return rc;
}

static int mid_mac(const struct mid_state *st, const uint8_t *msg, size_t len, uint8_t out[MAC_LEN])
{
	SHA256_CTX c = st->in;
	uint8_t inner[MAC_LEN];

	if (SHA256_Update(&c, msg, len) != 1 || SHA256_Final(inner, &c) != 1)
		return -1;
	c = st->out;
	if (SHA256_Update(&c, inner, sizeof inner) != 1 || SHA256_Final(out, &c) != 1)
		return -1;
	return 0;
}

static int hex2bin(const char *hex, uint8_t *out, size_t n)
{
	for (size_t i = 0; i < n; i++) {
		unsigned v;
		if (sscanf(hex + 2 * i, "%2x", &v) != 1)
			return -1;
		out[i] = (uint8_t)v;
	}
	return 0;
}

// make_jwt_input fills buf with a base64url-looking `header.payload` of len
// bytes, the shape of a JWT signing input. The header is the real HS256 one.
static void make_jwt_input(uint8_t *buf, size_t len)
{
	static const char hdr[] = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.";
	static const char alpha[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
	size_t h = sizeof hdr - 1;

	if (len < h + 1)
		h = len;
	memcpy(buf, hdr, h);
	for (size_t i = h; i < len; i++)
		buf[i] = (uint8_t)alpha[(i * 7u + 3u) % 64u];
}

enum form { FORM_EVP, FORM_REINIT, FORM_MID };

static const char *const FORM_NAME[] = {"hmac_evp", "hmac_reinit", "hmac_mid"};

static int mac_by(enum form f, const struct evp_state *ev, const struct evp_state *rv, const struct mid_state *mi, const uint8_t *msg,
		  size_t len, uint8_t out[MAC_LEN])
{
	switch (f) {
	case FORM_EVP: return evp_mac(ev, msg, len, out);
	case FORM_REINIT: return reinit_mac(rv, msg, len, out); // its own ctx: final() leaves it unfit to dup
	case FORM_MID: return mid_mac(mi, msg, len, out);
	}
	return -1;
}

static int selftest(void)
{
	// RFC 4231 test case 2: key "Jefe", data "what do ya want for nothing?".
	static const char want_hex[] = "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843";
	const uint8_t key[] = {'J', 'e', 'f', 'e'};
	const char *data = "what do ya want for nothing?";
	uint8_t want[MAC_LEN], got[MAC_LEN];
	struct evp_state ev = {0}, rv = {0};
	struct mid_state mi;
	int rc = 1;

	if (hex2bin(want_hex, want, MAC_LEN) != 0)
		return 1;
	if (evp_init(&ev, key, sizeof key) != 0 || evp_init(&rv, key, sizeof key) != 0 ||
	    mid_init(&mi, key, sizeof key) != 0) {
		fputs("selftest: init failed\n", stderr);
		goto out;
	}
	for (int f = FORM_EVP; f <= FORM_MID; f++) {
		// Twice, so the reused context of the reinit form is shown to reset.
		for (int rep = 0; rep < 2; rep++) {
			if (mac_by((enum form)f, &ev, &rv, &mi, (const uint8_t *)data, strlen(data), got) != 0 ||
			    CRYPTO_memcmp(got, want, MAC_LEN) != 0) {
				fprintf(stderr, "selftest: %s does not match RFC 4231 case 2\n", FORM_NAME[f]);
				goto out;
			}
		}
	}
	evp_free(&ev);
	evp_free(&rv);
	OPENSSL_cleanse(&mi, sizeof mi);

	// The three forms must agree on a 32-byte key and every length 0..300.
	uint8_t key32[KEY_LEN], msg[300], a[MAC_LEN], b[MAC_LEN];
	for (size_t i = 0; i < KEY_LEN; i++)
		key32[i] = (uint8_t)(i * 31u + 5u);
	if (evp_init(&ev, key32, KEY_LEN) != 0 || evp_init(&rv, key32, KEY_LEN) != 0 ||
	    mid_init(&mi, key32, KEY_LEN) != 0) {
		fputs("selftest: init failed for the 32-byte key\n", stderr);
		goto out;
	}
	for (size_t len = 0; len <= sizeof msg; len++) {
		for (size_t i = 0; i < len; i++)
			msg[i] = (uint8_t)(i * 13u + len);
		if (mac_by(FORM_EVP, &ev, &rv, &mi, msg, len, a) != 0) {
			fprintf(stderr, "selftest: evp failed at length %zu\n", len);
			goto out;
		}
		for (int f = FORM_REINIT; f <= FORM_MID; f++) {
			if (mac_by((enum form)f, &ev, &rv, &mi, msg, len, b) != 0 || CRYPTO_memcmp(a, b, MAC_LEN) != 0) {
				fprintf(stderr, "selftest: evp and %s differ at length %zu\n", FORM_NAME[f], len);
				goto out;
			}
		}
	}
	puts("selftest=ok");
	rc = 0;
out:
	evp_free(&ev);
	evp_free(&rv);
	OPENSSL_cleanse(&mi, sizeof mi);
	return rc;
}

static int run(enum form f, uint64_t iters, size_t msg_len)
{
	uint8_t key[KEY_LEN], msg[MAX_MSG], out[MAC_LEN];
	struct evp_state ev = {0};
	struct mid_state mi;
	uint8_t sink = 0;
	int rc = 1;

	for (size_t i = 0; i < KEY_LEN; i++)
		key[i] = (uint8_t)(i * 31u + 5u);
	make_jwt_input(msg, msg_len);
	if ((f == FORM_MID ? mid_init(&mi, key, KEY_LEN) : evp_init(&ev, key, KEY_LEN)) != 0) {
		fputs("init failed\n", stderr);
		goto out;
	}

	// The counter is written into the message every round so no call can be
	// hoisted, and the output is folded into sink so none can be dropped.
	uint64_t warm = iters / 10 + 1;
	uint64_t t0 = 0;
	struct cref_cpu c0 = {0};

	for (uint64_t i = 0; i < warm + iters; i++) {
		if (i == warm) {
			t0 = cref_now_ns();
			c0 = cref_cpu_now();
		}
		if (msg_len >= 8)
			memcpy(msg + msg_len - 8, &i, 8);
		if (mac_by(f, &ev, &ev, &mi, msg, msg_len, out) != 0) {
			fputs("mac failed\n", stderr);
			goto out;
		}
		sink ^= out[0];
	}
	uint64_t t1 = cref_now_ns();
	struct cref_cpu c1 = cref_cpu_now();
	cref_report(FORM_NAME[f], iters, t1 - t0, c0, c1);
	g_sink = sink;
	rc = 0;
out:
	if (f != FORM_MID)
		evp_free(&ev);
	else
		OPENSSL_cleanse(&mi, sizeof mi);
	return rc;
}

int main(int argc, char **argv)
{
	uint64_t iters = 2000000;
	size_t msg_len = 200;
	enum form f = FORM_EVP;

	if (argc < 2) {
		fputs("usage: hmac_bench selftest | evp|reinit|mid [-n iters] [-l len]\n", stderr);
		return 2;
	}
	if (strcmp(argv[1], "selftest") == 0)
		return selftest();
	if (strcmp(argv[1], "evp") == 0)
		f = FORM_EVP;
	else if (strcmp(argv[1], "reinit") == 0)
		f = FORM_REINIT;
	else if (strcmp(argv[1], "mid") == 0)
		f = FORM_MID;
	else {
		fputs("form must be evp, reinit or mid\n", stderr);
		return 2;
	}
	for (int i = 2; i + 1 < argc; i += 2) {
		if (strcmp(argv[i], "-n") == 0)
			iters = strtoull(argv[i + 1], nullptr, 10);
		else if (strcmp(argv[i], "-l") == 0)
			msg_len = strtoull(argv[i + 1], nullptr, 10);
		else {
			fprintf(stderr, "unknown option %s\n", argv[i]);
			return 2;
		}
	}
	if (iters == 0 || msg_len == 0 || msg_len > MAX_MSG) {
		fputs("bad -n or -l\n", stderr);
		return 2;
	}
	return run(f, iters, msg_len);
}
