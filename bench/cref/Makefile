# bench/cref - the C reference bench. Diagnostic only: no go.mod (so it stays
# out of scripts/ci/go-modules.sh and the cross-platform census), never a CI
# gate. See README.md.

CC      ?= gcc
CSTD    ?= -std=c23
OPT     ?= -O3 -march=native
WARN    := -Wall -Wextra -Wpedantic -Wshadow -Wconversion -Wformat=2 -Werror
DEFS    := -D_GNU_SOURCE
CFLAGS  += $(CSTD) $(DEFS) $(OPT) $(WARN) -g
BUILD   := build
SAN     := build/san

# picohttpparser, informational only. Pinned to a commit, checked by sha256;
# a mismatch deletes the file and fails the build.
PICO_COMMIT := 465a7ff09fbd3432fe56c673451f5460154d1f07
PICO_URL    := https://raw.githubusercontent.com/h2o/picohttpparser/$(PICO_COMMIT)
PICO_C_SHA  := 6480689711996bc9a219e26df27ac32ebbb7dce2f24a63a316d041b08f646199
PICO_H_SHA  := 1fc9074dd12418b2b91e55ef3a8279bccf8d3577b790dea6f2f908ab2a157f1c
VENDOR      := $(BUILD)/vendor

BINS := hmac_bench parse_test parse_bench echo_epoll echo_uring echo_client pairstat

.PHONY: all test sanitize tidy clean
all: $(addprefix $(BUILD)/,$(BINS))

$(BUILD) $(VENDOR) $(SAN):
	mkdir -p $@

# sha256 of a file, portable between GNU coreutils and BSD.
define sha256_of
$$(if command -v sha256sum >/dev/null 2>&1; then sha256sum $(1) | cut -d' ' -f1; else shasum -a 256 $(1) | cut -d' ' -f1; fi)
endef

$(VENDOR)/picohttpparser.c: | $(VENDOR)
	curl -fsSL -o $@ $(PICO_URL)/picohttpparser.c
	@got=$(call sha256_of,$@); if [ "$$got" != "$(PICO_C_SHA)" ]; then \
	  echo "picohttpparser.c: sha256 $$got, want $(PICO_C_SHA)" >&2; rm -f $@; exit 1; fi
$(VENDOR)/picohttpparser.h: | $(VENDOR)
	curl -fsSL -o $@ $(PICO_URL)/picohttpparser.h
	@got=$(call sha256_of,$@); if [ "$$got" != "$(PICO_H_SHA)" ]; then \
	  echo "picohttpparser.h: sha256 $$got, want $(PICO_H_SHA)" >&2; rm -f $@; exit 1; fi

# Third-party code: built at the same -O3 -march=native, without our warnings.
$(BUILD)/picohttpparser.o: $(VENDOR)/picohttpparser.c $(VENDOR)/picohttpparser.h
	$(CC) -std=gnu11 $(OPT) -w -c -o $@ $<

$(BUILD)/hmac_bench: src/hmac_bench.c src/cref.h | $(BUILD)
	$(CC) $(CFLAGS) -o $@ $< -lcrypto

$(BUILD)/parse_test: src/parse_test.c src/http_parse.c src/http_parse.h | $(BUILD)
	$(CC) $(CFLAGS) -o $@ src/parse_test.c src/http_parse.c

# pico_guard.c holds the #error that stops the build on a CPU target without
# SSE4.2: picohttpparser would silently fall back to its scalar path and the
# comparison would mean something else.
$(BUILD)/parse_bench: src/parse_bench.c src/pico_guard.c src/http_parse.c src/http_parse.h src/cref.h \
		$(BUILD)/picohttpparser.o $(VENDOR)/picohttpparser.h | $(BUILD)
	$(CC) $(CFLAGS) -I$(VENDOR) -o $@ src/parse_bench.c src/pico_guard.c src/http_parse.c \
		$(BUILD)/picohttpparser.o

$(BUILD)/echo_epoll: src/echo_epoll.c src/cref.h | $(BUILD)
	$(CC) $(CFLAGS) -o $@ $<

$(BUILD)/echo_uring: src/echo_uring.c src/cref.h | $(BUILD)
	$(CC) $(CFLAGS) -o $@ $< -luring

# Static, so the binary built here runs on the generator host whatever its libc.
$(BUILD)/echo_client: src/echo_client.c src/cref.h | $(BUILD)
	$(CC) $(CFLAGS) -static -o $@ $<

$(BUILD)/pairstat: src/pairstat.c | $(BUILD)
	$(CC) $(CFLAGS) -o $@ $< -lm

test: $(BUILD)/hmac_bench $(BUILD)/parse_test
	$(BUILD)/hmac_bench selftest
	$(BUILD)/parse_test

# The tests again under AddressSanitizer and UBSanitizer, at -O1 and without
# -march=native: what is checked is the logic, not the speed.
sanitize: | $(SAN)
	$(CC) $(CSTD) $(DEFS) -O1 -g $(WARN) -fsanitize=address,undefined -fno-sanitize-recover=all \
		-o $(SAN)/parse_test src/parse_test.c src/http_parse.c
	$(CC) $(CSTD) $(DEFS) -O1 -g $(WARN) -fsanitize=address,undefined -fno-sanitize-recover=all \
		-o $(SAN)/hmac_bench src/hmac_bench.c -lcrypto
	$(SAN)/parse_test
	$(SAN)/hmac_bench selftest

# clang-tidy needs picohttpparser.h (parse_bench.c includes it), so fetch it first.
tidy: $(VENDOR)/picohttpparser.h
	clang-tidy --quiet src/*.c -- $(CSTD) $(DEFS) -march=native -I$(VENDOR)

clean:
	rm -rf $(BUILD)
