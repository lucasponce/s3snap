# Makefile for s3snap.
#
# Run `make` or `make help` to list the available targets.

GO      ?= go
PKGS    ?= ./...
# Buckets for the opt-in integration test are created/deleted by the test itself;
# AWS credentials come from the standard SDK chain.
AWS_REGION ?= us-west-2

.DEFAULT_GOAL := help

## help: show this help
.PHONY: help
help:
	@echo "s3snap targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'

## all: format check, vet, build and test
.PHONY: all
all: fmt-check vet build test

## build: compile all packages
.PHONY: build
build:
	$(GO) build $(PKGS)

## test: run unit tests (in-memory, no AWS required)
.PHONY: test
test:
	$(GO) test $(PKGS)

## test-race: run unit tests with the race detector
.PHONY: test-race
test-race:
	$(GO) test -race $(PKGS)

## cover: run unit tests and report total coverage
.PHONY: cover
cover:
	$(GO) test -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -n 1

## test-integration: run the real-S3 integration test (needs AWS creds; opt-in)
.PHONY: test-integration
test-integration:
	S3SNAP_IT=1 AWS_REGION=$(AWS_REGION) $(GO) test -tags integration -run TestIntegration -v $(PKGS)

## vet: run go vet (including the integration-tagged files)
.PHONY: vet
vet:
	$(GO) vet $(PKGS)
	$(GO) vet -tags integration $(PKGS)

## fmt: format the source with gofmt
.PHONY: fmt
fmt:
	gofmt -w .

## fmt-check: fail if any file is not gofmt-clean
.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

## tidy: sync go.mod/go.sum
.PHONY: tidy
tidy:
	$(GO) mod tidy

## clean: remove build/test artifacts
.PHONY: clean
clean:
	$(GO) clean
	rm -f coverage.out
