.PHONY: fmt lint contracts generate check-generated test race security verify

fmt:
	gofmt -w cmd/gatehouse-control/main.go cmd/gatehouse-data/main.go

lint:
	@test -z "$$(gofmt -l cmd/gatehouse-control/main.go cmd/gatehouse-data/main.go)"
	go vet ./...
	buf lint

contracts:
	buf lint
	buf breaking --against '.git#branch=main'

generate:
	buf generate

check-generated: generate
	git diff --exit-code -- gen/
	test -z "$$(git status --porcelain --untracked-files=all -- gen/)"

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

security:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

verify: lint contracts check-generated test race security
