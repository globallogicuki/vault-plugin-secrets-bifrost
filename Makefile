PLUGIN_NAME := vault-plugin-secrets-bifrost
PLUGIN_DIR  := vault/plugins
MOUNT       := bifrost
DIST        := dist

# Bare version token used in dist filenames: X.Y.Z, no leading v. The `v`
# belongs to the git tag and to Vault's plugin catalogue, not to filenames.
# Defaults to the nearest tag so a hand-built binary cannot be mislabelled as
# a release; CI passes VERSION explicitly. See docs/12-build-and-release.md.
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --abbrev=0 2>/dev/null))
ifeq ($(strip $(VERSION)),)
VERSION := 0.0.0-dev
endif

# sha256sum on Linux, shasum -a 256 on macOS. Identical output format, so
# SHA256SUMS produced on either is verifiable on both.
SHA256SUM ?= $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo 'shasum -a 256')

COVERPROFILE     ?= coverage.out
# Keep in step with .github/workflows/ci.yml. Dependabot does not bump this.
GOLANGCI_VERSION ?= v2.13.1

.PHONY: build dist checksums register test testacc test-ci \
        fmt fmt-check vet lint lint-golangci lint-all mod-check clean

# Build the plugin binary into a directory Vault can load via -dev-plugin-dir.
build:
	@mkdir -p $(PLUGIN_DIR)
	go build -o $(PLUGIN_DIR)/$(PLUGIN_NAME) ./cmd/$(PLUGIN_NAME)

# Cross-compile for the platforms Vault runs on in Kubernetes. Vault plugins are
# native binaries, so the target must match the Vault container's OS/arch.
# CGO is disabled so the binary runs on the minimal Vault image; -trimpath and
# -ldflags="-s -w" keep builds reproducible and strip ~11MB of symbol tables.
# dist/ is cleared first so a binary left from a different VERSION cannot be
# picked up by a later release. See docs/12-build-and-release.md.
dist:
	@rm -rf $(DIST)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -trimpath -ldflags="-s -w" \
		-o $(DIST)/$(PLUGIN_NAME)_$(VERSION)_linux_amd64 ./cmd/$(PLUGIN_NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
		go build -trimpath -ldflags="-s -w" \
		-o $(DIST)/$(PLUGIN_NAME)_$(VERSION)_linux_arm64 ./cmd/$(PLUGIN_NAME)

# Write and print the SHA256 of each dist artefact, for
# `vault plugin register -sha256=`. Depends on dist, so one invocation is
# enough - calling `make dist checksums` would cross-compile twice.
checksums: dist
	cd $(DIST) && $(SHA256SUM) $(PLUGIN_NAME)_$(VERSION)_* | tee SHA256SUMS

# Register the built plugin with a running dev Vault and enable it at $(MOUNT).
# Requires VAULT_ADDR and VAULT_TOKEN in the environment.
register:
	vault plugin register \
		-sha256=$$($(SHA256SUM) $(PLUGIN_DIR)/$(PLUGIN_NAME) | cut -d' ' -f1) \
		secret $(PLUGIN_NAME)
	vault secrets enable -path=$(MOUNT) $(PLUGIN_NAME)

# Unit + backend (mock Bifrost) tests.
test:
	go test ./...

# Acceptance tests against a real/dockerised Bifrost.
# Requires BIFROST_ADDR and BIFROST_MANAGEMENT_TOKEN. Without VAULT_ACC set
# these skip, which is why plain `make test` is safe in CI.
testacc:
	VAULT_ACC=1 go test -v -count=1 ./internal/bifrost/...

# Packages to instrument for coverage: those that actually have tests.
#
# This is not merely a tidy-up. go1.25.7 - the version pinned in go.mod - fails
# with `go: no such tool "covdata"` when -coverprofile instruments a package
# that has no test files (here, ./cmd/...). The tool is built from GOROOT/src
# on demand, and that path is broken in this patch release; go1.25.12 handles
# the same build fine. Filtering to packages with tests sidesteps it without
# changing the toolchain, which would change the release binary's SHA256.
#
# Caveat: the coverage denominator therefore excludes untested packages, so the
# figure is coverage of tested packages, not of the whole module. Coverage is
# reported, not gated, so this is a reporting nuance rather than a weakened gate.
COVER_PKGS = $$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)

# What CI runs. -race needs cgo, so never set CGO_ENABLED=0 in the environment:
# it is set per-build inside `dist` only.
test-ci:
	go test -race -covermode=atomic -coverprofile=$(COVERPROFILE) -count=1 $(COVER_PKGS)

fmt:
	gofmt -w .

# `gofmt -l` lists offending files but exits 0, so it can never fail a build on
# its own. Assert on its output instead.
fmt-check:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt: these files need formatting (run 'make fmt'):"; \
		echo "$$out"; \
		exit 1; \
	fi

vet:
	go vet ./...

# Deliberately excludes lint-golangci: `make lint` must work on any machine
# with only the Go toolchain installed. CI runs both. See docs/08.
lint: fmt-check vet

lint-golangci:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found. Install $(GOLANGCI_VERSION):"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)"; \
		exit 1; }
	golangci-lint run

lint-all: lint lint-golangci

# Fails if go.mod/go.sum are not tidy, or if a module's contents do not match
# its recorded hash.
mod-check:
	go mod tidy -diff
	go mod verify

clean:
	rm -rf $(PLUGIN_DIR) $(DIST) $(COVERPROFILE)
