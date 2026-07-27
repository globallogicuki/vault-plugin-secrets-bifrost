PLUGIN_NAME := vault-plugin-secrets-bifrost
PLUGIN_DIR  := vault/plugins
MOUNT       := bifrost

# Build the plugin binary into a directory Vault can load via -dev-plugin-dir.
.PHONY: build
build:
	@mkdir -p $(PLUGIN_DIR)
	go build -o $(PLUGIN_DIR)/$(PLUGIN_NAME) ./cmd/$(PLUGIN_NAME)

# Register the built plugin with a running dev Vault and enable it at $(MOUNT).
# Requires VAULT_ADDR and VAULT_TOKEN in the environment.
.PHONY: register
register:
	vault plugin register \
		-sha256=$$(shasum -a 256 $(PLUGIN_DIR)/$(PLUGIN_NAME) | cut -d' ' -f1) \
		secret $(PLUGIN_NAME)
	vault secrets enable -path=$(MOUNT) $(PLUGIN_NAME)

# Unit + backend (mock Bifrost) tests.
.PHONY: test
test:
	go test ./...

# Acceptance tests against a real/dockerised Bifrost + Vault dev server.
# Requires BIFROST_ADDR and BIFROST_MANAGEMENT_TOKEN.
.PHONY: testacc
testacc:
	VAULT_ACC=1 go test -v -count=1 ./internal/bifrost/...

.PHONY: lint
lint:
	go vet ./...
	gofmt -l .

.PHONY: clean
clean:
	rm -rf $(PLUGIN_DIR)
