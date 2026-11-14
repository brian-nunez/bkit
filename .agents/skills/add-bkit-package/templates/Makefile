# Makefile for the __MODULE__ module.

MODULE_NAME := __MODULE__
REPOSITORY_ROOT := $(abspath $(CURDIR)/__REPOSITORY_ROOT__)

.DEFAULT_GOAL := help
.NOTPARALLEL: check

include $(REPOSITORY_ROOT)/make/development.mk

.PHONY: benchmarks benchmarks-actions benchmarks-nobuild build build-actions check clean format format-check help lint test tests tests-actions tests-nobuild tidy vet

help:
	@echo "$(MODULE_NAME) module developer commands:"
	@echo "  make doctor        Validate required local tool versions."
	@echo "  make tools         Install the pinned repository-local golangci-lint."
	@echo "  make format        Apply Go formatting."
	@echo "  make format-check  Report formatting differences without changing files."
	@echo "  make lint          Run the CI-equivalent Go linters."
	@echo "  make vet           Run go vet."
	@echo "  make tests         Build and run race-enabled tests."
	@echo "  make benchmarks    Build and run benchmarks."
	@echo "  make tidy          Tidy go.mod and go.sum."
	@echo "  make check         Run the complete local pre-PR validation."
	@echo "  make clean         Remove generated build and test artifacts."

check: doctor format-check lint vet tests

build: check-go
	@echo "Building $(MODULE_NAME)..."
	$(GO_BINARY) mod vendor
	$(GO_BINARY) build ./...

build-actions: build

tidy: check-go
	@echo "Tidying $(MODULE_NAME)..."
	$(GO_BINARY) mod tidy -go=$(GO_MOD_VERSION)

tests: build tests-nobuild

test: tests

tests-nobuild: check-go
	$(GO_BINARY) test -race -v -covermode=atomic -coverprofile=coverage.out ./...

tests-actions: tests-nobuild

benchmarks: build benchmarks-nobuild

benchmarks-nobuild: check-go
	$(GO_BINARY) test -run=^$$ -bench=. -benchmem ./...

benchmarks-actions: check-go
	$(GO_BINARY) test -run=^$$ -bench=. -race ./...

format: check-golangci-lint
	@echo "Formatting $(MODULE_NAME) Go source..."
	@if ! $(GOLANGCI_LINT_COMMAND) fmt --config "$(REPOSITORY_ROOT)/.golangci.yml"; then \
		echo "ERROR: formatting failed. Resolve the error above and rerun 'make format'."; \
		exit 1; \
	fi

format-check: check-golangci-lint
	@echo "Checking $(MODULE_NAME) Go formatting..."
	@if ! $(GOLANGCI_LINT_COMMAND) fmt --config "$(REPOSITORY_ROOT)/.golangci.yml" --diff; then \
		echo "ERROR: $(MODULE_NAME) Go formatting differs from repository policy."; \
		echo "Run 'make format', review the changes, and rerun 'make format-check'."; \
		exit 1; \
	fi

lint: check-golangci-lint
	@echo "Linting $(MODULE_NAME)..."
	@if ! $(GOLANGCI_LINT_COMMAND) run --config "$(REPOSITORY_ROOT)/.golangci.yml" --timeout=3m ./...; then \
		echo "ERROR: $(MODULE_NAME) lint failed. Fix the issues above and rerun 'make lint'."; \
		exit 1; \
	fi

vet: check-go
	@echo "Vetting $(MODULE_NAME)..."
	@if ! "$(GO_BINARY)" vet ./...; then \
		echo "ERROR: $(MODULE_NAME) go vet failed. Fix the issues above and rerun 'make vet'."; \
		exit 1; \
	fi

clean:
	@echo "Cleaning $(MODULE_NAME)..."
	rm -rf vendor/ coverage.out coverage.html
