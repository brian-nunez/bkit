# Makefile for the rdb/drivers/sqlite module.

MODULE_NAME := rdb/drivers/sqlite
REPOSITORY_ROOT := $(abspath $(CURDIR)/../../..)

# Package paths (an extended regular expression) left out of coverage-check
# and deadcode.
# Reserved for shared test-helper packages, such as a driver contract suite;
# every other package must reach 100% statement coverage.
COVERAGE_EXCLUDE ?=

.DEFAULT_GOAL := help
.NOTPARALLEL: check

include $(REPOSITORY_ROOT)/make/development.mk

.PHONY: benchmarks benchmarks-actions benchmarks-nobuild build build-actions check clean coverage-check deadcode format format-check help lint test tests tests-actions tests-nobuild tidy tidy-check vet vuln

help:
	@echo "$(MODULE_NAME) module developer commands:"
	@echo "  make doctor        Validate required local tool versions."
	@echo "  make tools         Install the pinned developer tools into .tools/."
	@echo "  make format        Apply Go formatting."
	@echo "  make format-check  Report formatting differences without changing files."
	@echo "  make lint          Run the CI-equivalent Go linters."
	@echo "  make vet           Run go vet."
	@echo "  make tests         Build and run race-enabled tests."
	@echo "  make coverage-check  Require 100% statement coverage from the last test run."
	@echo "  make deadcode      Fail on functions no test or caller reaches."
	@echo "  make vuln          Fail on known vulnerabilities in reachable code."
	@echo "  make benchmarks    Build and run benchmarks."
	@echo "  make tidy          Tidy go.mod and go.sum."
	@echo "  make tidy-check    Fail when go.mod or go.sum is not tidy."
	@echo "  make check         Run the complete local pre-PR validation."
	@echo "  make clean         Remove generated build and test artifacts."

check: doctor format-check lint vet tidy-check tests coverage-check deadcode vuln

build: check-go
	@echo "Building $(MODULE_NAME)..."
	$(GO_BINARY) mod vendor
	$(GO_BINARY) build ./...

build-actions: build

tidy: check-go
	@echo "Tidying $(MODULE_NAME)..."
	$(GO_BINARY) mod tidy -go=$(GO_MOD_VERSION)

tidy-check: check-go
	@if ! $(GO_BINARY) mod tidy -diff; then \
		echo "ERROR: $(MODULE_NAME) go.mod or go.sum is not tidy. Run 'make tidy' and commit the result."; \
		exit 1; \
	fi

tests: build tests-nobuild

test: tests

tests-nobuild: check-go
	$(GO_BINARY) test -race -v -covermode=atomic -coverprofile=coverage.out ./...

tests-actions: tests-nobuild

coverage-check: check-go
	@if [ ! -f coverage.out ]; then \
		echo "ERROR: coverage.out not found. Run 'make tests' before 'make coverage-check'."; \
		exit 1; \
	fi
	@if [ -n "$(COVERAGE_EXCLUDE)" ]; then \
		grep -vE '$(COVERAGE_EXCLUDE)' coverage.out > coverage.check.out; \
	else \
		cp coverage.out coverage.check.out; \
	fi
	@total="$$($(GO_BINARY) tool cover -func=coverage.check.out | awk '/^total:/ {print $$NF}')"; \
	if [ "$$total" != "100.0%" ]; then \
		echo "ERROR: $(MODULE_NAME) coverage is $${total:-unknown}; 100.0% is required."; \
		$(GO_BINARY) tool cover -func=coverage.check.out | awk '$$NF != "100.0%"'; \
		exit 1; \
	fi; \
	echo "$(MODULE_NAME) coverage: $$total"

deadcode: check-go
	@echo "Checking $(MODULE_NAME) for unreachable functions..."
	@out="$$($(TOOLS) deadcode -test ./...)" || exit 1; \
	if [ -n "$(COVERAGE_EXCLUDE)" ]; then \
		out="$$(printf '%s\n' "$$out" | awk -v skip='$(COVERAGE_EXCLUDE)' 'NF && ("/" $$0) !~ skip')"; \
	fi; \
	if [ -n "$$out" ]; then \
		printf '%s\n' "$$out"; \
		echo "ERROR: $(MODULE_NAME) has unreachable functions. Delete them or reach them from a test."; \
		exit 1; \
	fi

vuln: check-go
	@echo "Scanning $(MODULE_NAME) for known vulnerabilities..."
	@if ! $(TOOLS) govulncheck ./...; then \
		echo "ERROR: $(MODULE_NAME) calls code with a known vulnerability. Upgrade the affected module or Go version."; \
		exit 1; \
	fi

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
	rm -rf vendor/ coverage.out coverage.check.out coverage.html
