# Shared development settings and tool checks for the repository root and
# every module Makefile. Include it after setting REPOSITORY_ROOT.

REQUIRED_GO_VERSION ?= 1.27.4
REQUIRED_GOLANGCI_LINT_VERSION ?= 2.15.0
GO_MOD_VERSION ?= 1.26.0

# Pinned developer and CI tools. scripts/tools.sh reads these values and
# installs each tool into .tools/ on first use. A golangci-lint version change
# also updates the archive checksums in scripts/tools.sh.
GOVULNCHECK_VERSION ?= v1.10.0
DEADCODE_VERSION ?= v0.53.0
GORELEASE_VERSION ?= v0.0.0-20261120182113-8801fbdfac81
GREMLINS_VERSION ?= v0.6.0

REPOSITORY_ROOT ?= $(CURDIR)
GO_BINARY ?= go
GO_BINARY_PATH := $(shell command -v "$(GO_BINARY)" 2>/dev/null || printf '%s' "$(GO_BINARY)")
GO_BINARY_DIRECTORY := $(dir $(GO_BINARY_PATH))
TOOLS = PATH="$(GO_BINARY_DIRECTORY):$$PATH" "$(REPOSITORY_ROOT)/scripts/tools.sh"
GOLANGCI_LINT_COMMAND = $(TOOLS) golangci-lint

# Every module builds as a consumer sees it: from its own go.mod, never
# through a local go.work.
export GOWORK := off

.PHONY: check-go check-golangci-lint doctor print-go-mod-version print-required-go-version print-required-golangci-lint-version tools

check-go:
	@if ! [ -x "$(GO_BINARY)" ] && ! command -v "$(GO_BINARY)" >/dev/null 2>&1; then \
		echo "ERROR: Go $(REQUIRED_GO_VERSION) is required, but '$(GO_BINARY)' was not found."; \
		echo "Install the approved Go $(REQUIRED_GO_VERSION) toolchain, then run 'make doctor'."; \
		exit 1; \
	fi
	@actual_version="$$($(GO_BINARY) env GOVERSION 2>/dev/null)"; \
	expected_version="go$(REQUIRED_GO_VERSION)"; \
	if [ "$$actual_version" != "$$expected_version" ]; then \
		echo "ERROR: Go $(REQUIRED_GO_VERSION) is required; found $${actual_version:-unknown}."; \
		echo "Install the approved Go $(REQUIRED_GO_VERSION) toolchain, then run 'make doctor'."; \
		exit 1; \
	fi

check-golangci-lint: check-go
	@if ! $(GOLANGCI_LINT_COMMAND) version >/dev/null; then \
		echo "ERROR: golangci-lint $(REQUIRED_GOLANGCI_LINT_VERSION) could not be installed or run."; \
		echo "Run 'make tools' from the repository root and resolve the error it reports."; \
		exit 1; \
	fi

doctor: check-golangci-lint
	@echo "Developer environment is ready."
	@echo "  Go: $$($(GO_BINARY) env GOVERSION)"
	@$(TOOLS) versions | sed 's/^/  /'

tools: check-go
	@echo "Installing pinned developer tools in $(REPOSITORY_ROOT)/.tools..."
	@if ! $(TOOLS) install; then \
		echo "ERROR: failed to install the pinned developer tools."; \
		echo "Confirm access to GitHub releases and the Go module proxy, then rerun 'make tools'."; \
		exit 1; \
	fi
	@echo "Local developer tools are ready."

print-required-go-version:
	@echo "$(REQUIRED_GO_VERSION)"

print-required-golangci-lint-version:
	@echo "$(REQUIRED_GOLANGCI_LINT_VERSION)"

print-go-mod-version:
	@echo "$(GO_MOD_VERSION)"
