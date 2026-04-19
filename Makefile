SDK_MODULES := internal/kernel internal/core internal/service pkg/v1
GO ?= go

.PHONY: sdk-test sdk-lint sdk-tidy sdk-sync sdk-cover sdk-all sdk-release-check sdk-errs-audit

sdk-sync:
	$(GO) work sync

sdk-tidy:
	@for m in $(SDK_MODULES); do \
		echo "→ tidy $$m"; \
		(cd $$m && $(GO) mod tidy) || exit 1; \
	done

sdk-test:
	@for m in $(SDK_MODULES); do \
		echo "→ test $$m"; \
		(cd $$m && GOWORK=off $(GO) test -race -cover ./...) || exit 1; \
	done

sdk-lint:
	golangci-lint run ./...

sdk-cover:
	@for m in $(SDK_MODULES); do \
		echo "→ cover $$m"; \
		(cd $$m && GOWORK=off $(GO) test -coverprofile=coverage.out ./... && $(GO) tool cover -func=coverage.out | tail -1) || exit 1; \
	done

sdk-errs-audit:
	cd internal/kernel && GOWORK=off $(GO) test -run TestAudit ./errs/... -v

sdk-all: sdk-sync sdk-tidy sdk-lint sdk-errs-audit sdk-test

sdk-release-check:
	@echo "Tags format: internal/<layer>/vX.Y.Z  pkg/vN/vX.Y.Z"
	@git tag --list 'internal/*/v*' 'pkg/*/v*' | sort
