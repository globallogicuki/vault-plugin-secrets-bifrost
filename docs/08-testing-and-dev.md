# 08 - Testing & dev

## Layers

| Layer | What it proves | Tooling |
|-------|----------------|---------|
| Unit | Path logic, role→request mapping, error/idempotency handling | `go test`, table-driven |
| Backend (in-memory Vault) | Path CRUD, secret framing, renew/revoke callbacks against a **mock Bifrost** | Vault SDK `logical.TestBackendConfig` + `httptest` |
| Acceptance | Issue and revoke against a real Bifrost | `VAULT_ACC=1`, `Factory` + `logical.InmemStorage` |
| Manual/dev harness | Human smoke test | scripts + local binaries |

The first three all run under `go test ./...`; the acceptance layer self-skips when `VAULT_ACC` is unset, so a plain `go test` is always safe.

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

Two facts about the current implementation, both worth knowing before extending it:

- **No Vault server is needed.** `acceptance_test.go` builds the backend in-process with `Factory` plus `logical.InmemStorage`. The "real" half is Bifrost, not Vault. Run it with nothing but `BIFROST_ADDR`, `BIFROST_MANAGEMENT_TOKEN` and `VAULT_ACC=1`.
- **It is currently shallower than the design intends.** A `// TODO` marks the missing half: it does not yet call Bifrost with the issued `sk-bf-…` key, so it proves that issue-then-revoke returns no error, not that the key ever worked or later stopped working. Closing that TODO is what makes this layer meaningful.

**No CI workflow runs them.** They need a live Bifrost and a real management token, and the safe triggers are limited: a `pull_request`-triggered run on a fork would expose the token to untrusted code, so `workflow_dispatch` or a scheduled run against a dedicated test tenant is the only sound option. Until there is a Bifrost instance to point at, `make testacc` is a local tool.

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

## CI

Three workflows, all thin wrappers over `make` so build flags exist in one place only. See [12](./12-build-and-release.md) for the release side.

### Every pull request and push to main - `.github/workflows/ci.yml`

| Job | What it runs |
|-----|--------------|
| `lint` | `make lint` (gofmt assertion + `go vet`), `make mod-check`, then `golangci-lint` |
| `test` | `make test-ci` - race detector on, coverage summarised in the run |
| `build` | `make build`, then the real `make dist`, then asserts both binaries are statically linked ELF |

Three jobs rather than one, so a lint failure cannot mask a test failure. The `build` job calls the release build path itself, so a cross-compilation break surfaces on the pull request rather than on a tag.

**No Go version matrix.** `go.mod` pins the toolchain, this is a single binary artefact rather than a library consumed at many Go versions, and the release binary's checksum is toolchain-dependent - so one pinned toolchain is the point. `GOTOOLCHAIN: local` is set so a mismatch fails loudly instead of silently downloading a different toolchain.

### On a merge to main - `.github/workflows/release-please.yml`

Reads the Conventional Commits since the last release and keeps a `chore(main): release X.Y.Z` pull request up to date. Merging *that* PR tags the release and calls `release.yml`. Nothing publishes on a plain merge to `main`.

The practical consequence for day-to-day work: **commit types now decide version numbers.** `feat:` bumps the minor, `fix:` the patch, and `docs:`/`ci:`/`chore:` release nothing. The commit convention in `CLAUDE.md` was already required; it is now load-bearing. Which types release nothing is set by the hidden `changelog-sections` entries in `release-please-config.json`, not by release-please's own defaults - do not make one visible without reading [12](./12-build-and-release.md) first.

`CHANGELOG.md` and `.release-please-manifest.json` are generated. Do not edit them by hand - the next release commit will overwrite both.

### Building and publishing - `.github/workflows/release.yml`

Guards, then `make lint` and `make test-ci`, then `make checksums`, then SBOM and provenance, then the release. Called by `release-please.yml`, and also triggerable by a hand-pushed `vX.Y.Z` tag or a `workflow_dispatch` dry run. Full description in [12](./12-build-and-release.md).

### Linting

| Tool | How it arrives |
|------|----------------|
| `gofmt` | `make fmt-check`, plus golangci-lint's `formatters` block |
| `goimports` | golangci-lint's `formatters` block, with `local-prefixes` set to the module path |
| `go vet` | `make vet` |
| golangci-lint (v2 config, `.golangci.yml`) | `golangci/golangci-lint-action` in CI; `make lint-golangci` locally |

`make lint` **deliberately excludes golangci-lint**: it must work on any machine with only the Go toolchain installed. `make lint-all` runs both, and CI runs both, so nothing is lost. Do not "fix" this by adding the dependency.

`gofmt -l` lists offending files but exits 0, so `make fmt-check` asserts on its output rather than its exit status. That is the only reason the target is a shell block instead of one command.

### Bumped by hand

Dependabot covers Go modules and action versions. These are deliberately not automated:

| Thing | Where | Why manual |
|-------|-------|------------|
| The `go` directive | `go.mod` | Changes the release binary's SHA256. A toolchain bump is a release decision. |
| golangci-lint version | `GOLANGCI_VERSION` in `Makefile`, `version:` in `ci.yml` | Duplicated in two files; a new minor can add linters and turn a green build red. |
| Vault chart and image versions | `docs/11-kubernetes-deployment.md` | Documentation, not a dependency of this repo. |

Note that go1.25.7 - the pinned patch release - fails with `go: no such tool "covdata"` when `-coverprofile` instruments a package that has no test files. `make test-ci` therefore instruments only packages that have tests. The comment on `COVER_PKGS` in the `Makefile` records the detail; the consequence is that the reported coverage figure is of tested packages, not of the whole module.

## Makefile targets

| Target | Action |
|--------|--------|
| `build` | Compile the plugin into `vault/plugins/` for the host platform |
| `dist` | Cross-compile `linux/amd64` + `linux/arm64` into `dist/` with release flags |
| `checksums` | `dist`, plus `dist/SHA256SUMS`. Depends on `dist`, so one invocation is enough |
| `register` | `vault plugin register` + `secrets enable` against a dev Vault |
| `test` | Unit + backend tests (`go test ./...`) |
| `test-ci` | What CI runs: race detector, atomic coverage into `coverage.out` |
| `testacc` | `VAULT_ACC=1 go test` - needs `BIFROST_ADDR` + `BIFROST_MANAGEMENT_TOKEN` |
| `fmt` | `gofmt -w .` |
| `fmt-check` | Fail if any file needs formatting |
| `vet` | `go vet ./...` |
| `lint` | `fmt-check` + `vet`. No tooling beyond Go required |
| `lint-golangci` | `golangci-lint run`, with an install hint if it is missing |
| `lint-all` | `lint` + `lint-golangci` |
| `mod-check` | `go mod tidy -diff` + `go mod verify` |
| `clean` | Remove `vault/plugins/`, `dist/`, `coverage.out` |

`VERSION` defaults to the nearest git tag with the leading `v` stripped, falling back to `0.0.0-dev`, so a hand-built binary cannot be mislabelled as a release.

## Related

- [12](./12-build-and-release.md) - build flags, release pipeline, reproducibility
- [09](./09-roadmap.md) - phased delivery
