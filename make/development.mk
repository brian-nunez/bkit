# Shared development settings and tool checks for the repository root and
# every module Makefile. Include it after setting REPOSITORY_ROOT.

REQUIRED_GO_VERSION ?= 1.27.4
REQUIRED_GOLANGCI_LINT_VERSION ?= 2.15.0
GO_MOD_VERSION ?= 1.26.0

REPOSITORY_ROOT ?= $(CURDIR)
GO_BINARY ?= go
GO_BINARY_PATH := $(shell command -v "$(GO_BINARY)" 2>/dev/null || printf '%s' "$(GO_BINARY)")
GO_BINARY_DIRECTORY := $(dir $(GO_BINARY_PATH))
LOCAL_TOOLS_DIRECTORY := $(REPOSITORY_ROOT)/.tools/bin
LOCAL_GOLANGCI_LINT := $(LOCAL_TOOLS_DIRECTORY)/golangci-lint
GOLANGCI_LINT_BINARY ?= $(if $(wildcard $(LOCAL_GOLANGCI_LINT)),$(LOCAL_GOLANGCI_LINT),golangci-lint)
GOLANGCI_LINT_COMMAND = PATH="$(GO_BINARY_DIRECTORY):$$PATH" "$(GOLANGCI_LINT_BINARY)"
GOLANGCI_LINT_PACKAGE := github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(REQUIRED_GOLANGCI_LINT_VERSION)

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
	@if ! [ -x "$(GOLANGCI_LINT_BINARY)" ] && ! command -v "$(GOLANGCI_LINT_BINARY)" >/dev/null 2>&1; then \
		echo "ERROR: golangci-lint $(REQUIRED_GOLANGCI_LINT_VERSION) is required, but it was not found."; \
		echo "Run 'make tools' from the repository root to install the pinned local version."; \
		exit 1; \
	fi
	@actual_version="$$($(GOLANGCI_LINT_COMMAND) version 2>/dev/null | sed -n 's/^golangci-lint has version \([^ ]*\).*/\1/p')"; \
	if [ "$$actual_version" != "$(REQUIRED_GOLANGCI_LINT_VERSION)" ]; then \
		echo "ERROR: golangci-lint $(REQUIRED_GOLANGCI_LINT_VERSION) is required; found $${actual_version:-unknown}."; \
		echo "Run 'make tools' from the repository root to install the pinned local version."; \
		exit 1; \
	fi

doctor: check-golangci-lint
	@echo "Developer environment is ready."
	@echo "  Go: $$($(GO_BINARY) env GOVERSION)"
	@echo "  golangci-lint: $(REQUIRED_GOLANGCI_LINT_VERSION)"
	@echo "  golangci-lint binary: $$(command -v "$(GOLANGCI_LINT_BINARY)" 2>/dev/null || echo "$(GOLANGCI_LINT_BINARY)")"

tools: check-go
	@echo "Installing golangci-lint $(REQUIRED_GOLANGCI_LINT_VERSION) in $(LOCAL_TOOLS_DIRECTORY)..."
	@mkdir -p "$(LOCAL_TOOLS_DIRECTORY)"
	@if ! GOBIN="$(LOCAL_TOOLS_DIRECTORY)" \
		$(GO_BINARY) install "$(GOLANGCI_LINT_PACKAGE)"; then \
		echo "ERROR: failed to install golangci-lint $(REQUIRED_GOLANGCI_LINT_VERSION)."; \
		echo "Confirm access to the Go module proxy and checksum database, then rerun 'make tools'."; \
		exit 1; \
	fi
	@$(MAKE) --no-print-directory check-golangci-lint
	@echo "Local developer tools are ready."

print-required-go-version:
	@echo "$(REQUIRED_GO_VERSION)"

print-required-golangci-lint-version:
	@echo "$(REQUIRED_GOLANGCI_LINT_VERSION)"

print-go-mod-version:
	@echo "$(GO_MOD_VERSION)"
