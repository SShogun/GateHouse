GO_VERSION := $(shell sed -n 's/^go //p' go.mod)
GO_TOOLCHAIN ?= go$(GO_VERSION)
GO = GOTOOLCHAIN=$(GO_TOOLCHAIN) go
STATICCHECK_VERSION ?= v0.7.0
GOVULNCHECK_VERSION ?= v1.8.0
BUF_BASE_BRANCH ?= main
BUF_BASE_REF ?=
GO_FILES = $(shell find . -type f -name '*.go' -not -path './.git/*' -not -path './.omx/*')

.PHONY: fmt fmt-check vet staticcheck lint contracts compatibility-test generate check-generated mod-verify test race security verify

fmt:
	gofmt -w $(GO_FILES)

fmt-check:
	@test -z "$$(gofmt -l $(GO_FILES))"

vet:
	$(GO) vet ./...

staticcheck:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

lint: fmt-check vet staticcheck

contracts:
	buf lint
	@if [ -n "$(BUF_BASE_REF)" ]; then \
		buf breaking --against ".git#ref=$(BUF_BASE_REF)"; \
	else \
		buf breaking --against ".git#branch=$(BUF_BASE_BRANCH)"; \
	fi

compatibility-test:
	./scripts/check-buf-breaking.sh

generate:
	buf generate

check-generated: generate
	git diff --exit-code -- gen/
	test -z "$$(git status --porcelain --untracked-files=all -- gen/)"

mod-verify:
	$(GO) mod verify

test:
	$(GO) test ./... -count=1 -shuffle=on

race:
	$(GO) test -race ./... -count=1 -shuffle=on

security:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

verify: fmt-check vet staticcheck contracts compatibility-test check-generated mod-verify test race security
