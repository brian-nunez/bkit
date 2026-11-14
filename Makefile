# COMPONENTS lists every independently versioned Go module, in release order:
# a module follows every bkit module it depends on, and a driver follows its
# parent.
COMPONENTS = objex objex/drivers/memory objex/drivers/filesystem objex/drivers/s3

REPOSITORY_ROOT := $(CURDIR)

.DEFAULT_GOAL := help
.NOTPARALLEL: check

include make/development.mk

.PHONY: build build-actions check check-components clean format format-check get-all-release-packages help lint test tests unit vet

help:
	@echo "bkit developer commands:"
	@echo "  make doctor            Validate required local tool versions"
	@echo "  make tools             Install the pinned local golangci-lint"
	@echo "  make format            Apply repository Go formatting"
	@echo "  make format-check      Report formatting differences without changing files"
	@echo "  make lint              Run the CI-equivalent Go linters for every module"
	@echo "  make vet               Run go vet for every module"
	@echo "  make tests             Build and run race-enabled tests for every module"
	@echo "  make check-components  Validate module registration and release configuration"
	@echo "  make check             Run the complete local pre-PR validation"
	@echo "  make clean             Remove generated build and test artifacts"

check: doctor check-components format-check lint vet tests

check-components:
	@scripts/check-components.sh

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
	for dir in $(COMPONENTS); do \
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
