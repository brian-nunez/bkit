# COMPONENTS lists every independently versioned Go module, in release order:
# a module follows every bkit module it depends on, and a driver follows its
# parent.
COMPONENTS = objex objex/drivers/memory objex/drivers/filesystem objex/drivers/s3 gate loom settings settings/drivers/yaml settings/drivers/toml settings/drivers/vault settings/drivers/consul rdb rdb/drivers/sqlite rdb/drivers/postgres rdb/drivers/mysql stash stash/drivers/memory stash/drivers/database stash/drivers/redis stash/drivers/valkey

REPOSITORY_ROOT := $(CURDIR)

# EXAMPLES lists the unreleased example modules. They stay out of COMPONENTS
# and may use a replace directive, but they build, vet, lint, and test like
# every other module.
EXAMPLES = $(patsubst ./%/go.mod,%,$(shell find . -path '*/examples/*' -name go.mod -not -path '*/vendor/*' | LC_ALL=C sort))

.DEFAULT_GOAL := help
.NOTPARALLEL: check

include make/development.mk

.PHONY: build build-actions check check-components clean coverage-check deadcode examples format format-check get-all-release-packages guardrail-check help lint test test-scripts tests tidy-check unit vet vuln

help:
	@echo "bkit developer commands:"
	@echo "  make doctor            Validate required local tool versions"
	@echo "  make tools             Install the pinned developer tools into .tools/"
	@echo "  make format            Apply repository Go formatting"
	@echo "  make format-check      Report formatting differences without changing files"
	@echo "  make lint              Run the CI-equivalent Go linters for every module"
	@echo "  make vet               Run go vet for every module"
	@echo "  make tests             Build and run race-enabled tests for every module"
	@echo "  make coverage-check    Require 100% statement coverage for every module"
	@echo "  make deadcode          Fail on unreachable functions in every module"
	@echo "  make vuln              Fail on known vulnerabilities in every module"
	@echo "  make tidy-check        Fail when any go.mod or go.sum is not tidy"
	@echo "  make examples          Build, vet, and test the example modules"
	@echo "  make check-components  Validate module registration and release configuration"
	@echo "  make test-scripts      Run the tests for the scripts in scripts/"
	@echo "  make guardrail-check   Audit this branch for guardrail changes and bypasses"
	@echo "  make check             Run the complete local pre-PR validation"
	@echo "  make clean             Remove generated build and test artifacts"

check: doctor check-components test-scripts format-check lint vet tidy-check tests coverage-check deadcode vuln examples

check-components:
	@scripts/check-components.sh

test-scripts:
	@bash scripts/guardrail-check_test.sh

# Exits 1 when the branch changes a guardrail; set GUARDRAIL_APPROVED=true
# after review. CI runs the same script in guardrails.yml.
guardrail-check:
	@bash scripts/guardrail-check.sh

tests: build unit

test: tests

build: check-go
	@echo "Building all modules..."
	$(GO_BINARY) mod vendor
	@for dir in $(COMPONENTS); do \
		$(MAKE) -C $$dir build || exit 1; \
	done

build-actions: check-go
	@echo "Building all modules in CI..."
	$(GO_BINARY) mod vendor
	@for dir in $(COMPONENTS); do \
		$(MAKE) -C $$dir build-actions || exit 1; \
	done

unit: check-go
	@for dir in $(COMPONENTS); do \
		$(MAKE) -C $$dir tests-actions || exit 1; \
	done

coverage-check: check-go
	@for dir in $(COMPONENTS); do \
		$(MAKE) --no-print-directory -C $$dir coverage-check || exit 1; \
	done

deadcode: check-go
	@for dir in $(COMPONENTS); do \
		$(MAKE) --no-print-directory -C $$dir deadcode || exit 1; \
	done

vuln: check-go
	@for dir in $(COMPONENTS); do \
		$(MAKE) --no-print-directory -C $$dir vuln || exit 1; \
	done

tidy-check: check-go
	@echo "Checking that every go.mod and go.sum is tidy..."
	@if ! $(GO_BINARY) mod tidy -diff; then \
		echo "ERROR: the root go.mod is not tidy. Run 'go mod tidy' and commit the result."; \
		exit 1; \
	fi
	@for dir in $(COMPONENTS); do \
		$(MAKE) --no-print-directory -C $$dir tidy-check || exit 1; \
	done
	@set -e; \
	for dir in $(EXAMPLES); do \
		if ! (cd "$$dir" && $(GO_BINARY) mod tidy -diff); then \
			echo "ERROR: $$dir go.mod is not tidy. Run 'go mod tidy' in $$dir and commit the result."; \
			exit 1; \
		fi; \
	done

examples: check-go
	@set -e; \
	for dir in $(EXAMPLES); do \
		echo "Building, vetting, and testing $$dir..."; \
		(cd "$$dir" && $(GO_BINARY) build -o /dev/null ./... && $(GO_BINARY) vet ./... && $(GO_BINARY) test -race ./...); \
	done

format: check-golangci-lint
	@echo "Formatting Go source..."
	@if ! $(GOLANGCI_LINT_COMMAND) fmt --config "$(REPOSITORY_ROOT)/.golangci.yml"; then \
		echo "ERROR: formatting failed. Resolve the error above and rerun 'make format'."; \
		exit 1; \
	fi

format-check: check-golangci-lint
	@echo "Checking Go formatting..."
	@if ! $(GOLANGCI_LINT_COMMAND) fmt --config "$(REPOSITORY_ROOT)/.golangci.yml" --diff; then \
		echo "ERROR: Go formatting differs from repository policy."; \
		echo "Run 'make format', review the changes, and rerun 'make format-check'."; \
		exit 1; \
	fi

lint: check-golangci-lint
	@echo "Linting repository root..."
	@if ! $(GOLANGCI_LINT_COMMAND) run --config "$(REPOSITORY_ROOT)/.golangci.yml" --timeout=3m ./...; then \
		echo "ERROR: lint failed in the repository root. Fix the issues above and rerun 'make lint'."; \
		exit 1; \
	fi
	@set -e; \
	for dir in $(COMPONENTS) $(EXAMPLES); do \
		echo "Linting $$dir..."; \
		if ! (cd "$$dir" && $(GOLANGCI_LINT_COMMAND) run --config "$(REPOSITORY_ROOT)/.golangci.yml" --timeout=3m ./...); then \
			echo "ERROR: lint failed in $$dir. Fix the issues above and rerun 'make lint'."; \
			exit 1; \
		fi; \
	done

vet: check-go
	@echo "Vetting repository root..."
	@if ! "$(GO_BINARY)" vet ./...; then \
		echo "ERROR: go vet failed in the repository root. Fix the issues above and rerun 'make vet'."; \
		exit 1; \
	fi
	@set -e; \
	for dir in $(COMPONENTS); do \
		echo "Vetting $$dir..."; \
		if ! (cd "$$dir" && "$(GO_BINARY)" vet ./...); then \
			echo "ERROR: go vet failed in $$dir. Fix the issues above and rerun 'make vet'."; \
			exit 1; \
		fi; \
	done

clean:
	@for dir in $(COMPONENTS); do \
		$(MAKE) -C $$dir clean || exit 1; \
	done
	rm -rf vendor/ coverage.out coverage.html

get-all-release-packages:
	@jq -cn '$$ARGS.positional' --args $(COMPONENTS)
