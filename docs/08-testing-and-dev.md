# 08 - Testing & dev

## Layers

| Layer | What it proves | Tooling |
|-------|----------------|---------|
| Unit | Path logic, role→request mapping, error/idempotency handling | `go test`, table-driven |
| Backend (in-memory Vault) | Path CRUD, secret framing, renew/revoke callbacks against a **mock Bifrost** | Vault SDK `logical.TestBackendConfig` + `httptest` |
| Acceptance | Real Vault + real (or dockerised) Bifrost end-to-end | `VAULT_ACC=1`, `dev` mode |
| Manual/dev harness | Human smoke test | scripts + local binaries |

## Mock Bifrost server

A small `httptest.Server` implementing just the endpoints in [04](./04-bifrost-integration.md):

- `POST /api/governance/virtual-keys` → returns `{ virtual_key: { id, value } }` with a generated id.
- `DELETE /api/governance/virtual-keys/{id}` → 200, or 404 for unknown ids (to test idempotent revoke).
- Optional fault injection (5xx, latency, 401) to exercise WAL rollback, retryable revoke, and auth-failure paths.

Backend tests drive the engine through `logical.Request`s (`config` write → `roles` write → `creds` read → simulate lease revoke) and assert the mock saw the expected calls.

## Acceptance tests

Gated behind `VAULT_ACC` so they don't run in normal `go test`:

```go
func TestAcc_IssueAndRevoke(t *testing.T) {
    if os.Getenv("VAULT_ACC") == "" { t.Skip("VAULT_ACC not set") }
    // needs BIFROST_ADDR + BIFROST_MANAGEMENT_TOKEN
}
```

They: write real `config`, create a role, read `creds/<role>`, call Bifrost with the returned `sk-bf-…` to confirm it works, revoke the lease, then confirm the key no longer works.

## Local dev harness

```sh
# 1. build the plugin into a dir Vault can load
make build            # → vault/plugins/vault-plugin-secrets-bifrost

# 2. run Vault in dev mode pointed at that dir
vault server -dev -dev-root-token-id=root \
    -dev-plugin-dir=./vault/plugins &

# 3. register + enable
export VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root
make register         # plugin register + secrets enable -path=bifrost

# 4. configure against a local/staging Bifrost and try it
vault write bifrost/config address=$BIFROST_ADDR management_token=$BIFROST_TOKEN
vault write bifrost/roles/demo provider_configs='[{"provider":"openai","allowed_models":["gpt-4o"]}]' ttl=5m
vault read  bifrost/creds/demo
```

A `docker-compose.yml` (dev) can stand up Bifrost + Vault together for one-command E2E.

## CI outline

- `go vet`, `gofmt`/`goimports`, `golangci-lint`.
- `go test ./...` (unit + backend/mock) on every push.
- Acceptance tests run in a dedicated job with a dockerised Bifrost + Vault dev server (nightly or on-label, since they need real services).
- Build matrix: linux/amd64, linux/arm64, darwin/arm64 (Go plugin binaries are platform-specific; no Windows).
- Release: tagged builds produce per-platform binaries + sha256 sums for `vault plugin register`.

## Makefile targets (planned)

| Target | Action |
|--------|--------|
| `build` | compile plugin into `vault/plugins/` |
| `register` | `vault plugin register` + `secrets enable` |
| `test` | unit + backend tests |
| `testacc` | `VAULT_ACC=1 go test` |
| `lint` | vet + linters |
| `clean` | remove build artifacts |
