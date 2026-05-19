.PHONY: sdk-sync sdk-tidy sdk-test sdk-lint sdk-cover sdk-errs-audit sdk-codec-bench sdk-bench-md sdk-all sdk-release-check \
        test build lint cover tidy bench

# `make` with no args runs the full pipeline (sync → lint → errs-audit → test).
.DEFAULT_GOAL := sdk-all

# ── Bazel wrappers ─────────────────────────────────────────────────────
# Every sdk-* target below shells to `bazel`; the source of truth is
# .bazelrc (named configs: race / pure / coverage / ci) + MODULE.bazel.
# Keep the wrappers minimal — complex pipelines belong in .bazelrc.

sdk-sync:
	bazel mod tidy
	bazel run //:gazelle

sdk-tidy: sdk-sync

sdk-test:
	bazel test --config=race //...

sdk-lint:
	bazel mod tidy
	bazel run //:gazelle -- -mode=diff
	@git diff --exit-code MODULE.bazel **/BUILD.bazel

sdk-cover:
	bazel coverage --combined_report=lcov //...
	@echo "LCOV report: $$(bazel info output_path)/_coverage/_coverage_report.dat"

sdk-errs-audit:
	bazel test --config=race //internal/kernel/errs:errs_test

sdk-codec-bench:
	bazel test //pkg/v1/codec:codec_bench_test \
		--test_arg=-test.bench=. \
		--test_arg=-test.benchmem \
		--test_arg=-test.run=^$$ \
		--test_arg=-test.benchtime=2s \
		--test_output=streamed

# Regenerates BENCH.md inside every package that carries a Bazel
# `benchmark`-tagged go_test target. Stamps a reproducibility envelope
# (CPU, RAM, OS, Go toolchain, git SHA, timestamp) at the top of each
# report so cross-machine deltas can be evaluated honestly.
sdk-bench-md:
	bash scripts/bench/gen-bench-md.sh

sdk-all: sdk-sync sdk-lint sdk-errs-audit sdk-test

sdk-release-check:
	@echo "Tags format: internal/<layer>/vX.Y.Z  pkg/vN/vX.Y.Z"
	@git tag --list 'internal/*/v*' 'pkg/*/v*' | sort

# ── Short aliases (map to sdk-* targets for ergonomic local use) ───────
test:  sdk-test
lint:  sdk-lint
cover: sdk-cover
tidy:  sdk-tidy
bench: sdk-bench-md

build:
	bazel build //...
