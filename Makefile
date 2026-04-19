.PHONY: sdk-sync sdk-tidy sdk-test sdk-lint sdk-cover sdk-errs-audit sdk-all sdk-release-check

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

sdk-all: sdk-sync sdk-lint sdk-errs-audit sdk-test

sdk-release-check:
	@echo "Tags format: internal/<layer>/vX.Y.Z  pkg/vN/vX.Y.Z"
	@git tag --list 'internal/*/v*' 'pkg/*/v*' | sort
