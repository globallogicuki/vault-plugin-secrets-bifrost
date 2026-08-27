# vault-plugin-secrets-bifrost

[![CI](https://github.com/globallogicuki/vault-plugin-secrets-bifrost/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/globallogicuki/vault-plugin-secrets-bifrost/actions/workflows/ci.yml)
[![Licence: MPL-2.0](https://img.shields.io/badge/licence-MPL--2.0-blue.svg)](./LICENSE)

> **Status: 🚧 Phase 1 (MVP) in progress.** The dynamic virtual-key path (`config`, `roles/<name>`, `creds/<role>`) is implemented with unit and backend tests against a mock Bifrost. Provider keys and static-role rotation are future phases (see [`docs/09-roadmap.md`](./docs/09-roadmap.md)).

A [HashiCorp Vault](https://www.vaultproject.io/) **secrets engine** plugin that dynamically manages credentials for [Bifrost](https://docs.getbifrost.ai/overview), the high-performance AI gateway.

Vault becomes the control plane for Bifrost access. Rather than long-lived, hand-distributed API keys, an application asks Vault for a Bifrost credential, uses it, and Vault revokes it when the lease expires. The MVP centres on Bifrost **virtual keys** (governance entities carrying budgets, rate limits, and per-provider scope); **provider API keys** follow as a second path.

## ⚠️ This is *not* Bifrost's built-in Vault backend

Bifrost already ships a native, enterprise **secret-consumption** feature: at runtime it reads `vault.<path>` references from a HashiCorp Vault (KV v2) to resolve provider keys. That makes **Bifrost a Vault *client***.

**This project runs the opposite way.** Here, **Vault is the *client* of Bifrost's management API**, *managing* (creating, leasing, revoking, rotating) Bifrost credentials.

| | Direction | Who calls whom |
|---|---|---|
| Bifrost native `hashicorp-vault` backend | Bifrost **reads** secrets from Vault | Bifrost → Vault |
| **This plugin** | Vault **manages** Bifrost credentials | Vault → Bifrost management API |

If you simply want to store existing provider keys in Vault and have Bifrost read them, use Bifrost's [built-in secret management](https://docs.getbifrost.ai/enterprise/secret-management) - not this plugin.

## Architecture

```mermaid
flowchart TD
    app["Application<br/>vault read bifrost/creds/app"]
    subgraph vault["Vault"]
        plugin["vault-plugin-secrets-bifrost<br/>• config (mgmt token, addr)<br/>• roles/ (VK scope template)<br/>• creds/ (dynamic issue)"]
    end
    api["Bifrost management API<br/>POST /api/governance/virtual-keys (issue)<br/>DELETE /api/governance/virtual-keys/:id (revoke)"]

    app -->|read creds| plugin
    plugin -->|lease + sk-bf-… value| app
    plugin -->|"Authorization: Bearer &lt;mgmt token&gt;"| api
```

On `vault read bifrost/creds/<role>`, the engine asks Bifrost to create a virtual key scoped by the role, returns the `sk-bf-…` value to the caller, and ties it to a Vault lease. When the lease expires or is revoked, the engine deletes the virtual key in Bifrost.

## Documentation

The full design framework lives in [`docs/`](./docs). Start with [`docs/README.md`](./docs/README.md), which sets the reading order. Highlights:

- [Overview & goals](./docs/01-overview.md)
- [Architecture](./docs/02-architecture.md)
- [Vault plugin interface](./docs/03-vault-plugin-interface.md)
- [Bifrost integration](./docs/04-bifrost-integration.md)
- [Secrets engine paths](./docs/05-secret-engine-paths.md)
- [Lease lifecycle](./docs/06-lease-lifecycle.md)
- [Security](./docs/07-security.md)
- [Testing & dev](./docs/08-testing-and-dev.md)
- [Roadmap](./docs/09-roadmap.md)
- [API specification](./docs/10-api-spec.md)

## Quickstart

Requires Go (the toolchain version is pinned in `go.mod`) and a Vault binary. The commands below use a local dev Vault and a mock or real Bifrost.

```sh
# 1. Build the plugin into a directory Vault can load.
make build            # -> vault/plugins/vault-plugin-secrets-bifrost

# 2. Run Vault in dev mode pointed at that directory.
vault server -dev -dev-root-token-id=root -dev-plugin-dir=./vault/plugins &
export VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root

# 3. Register the plugin and enable it at the bifrost/ mount.
make register

# 4. Configure the Bifrost connection (management bearer token).
vault write bifrost/config \
    address=https://bifrost.internal:8080 \
    management_token=$BIFROST_MANAGEMENT_TOKEN

# 5. Define a role templating the virtual key's scope.
vault write bifrost/roles/web-app \
    provider_configs='[{"provider":"openai","allowed_models":["gpt-4o"],"weight":1}]' \
    rate_limit='{"request_max_limit":1000,"request_reset_duration":"1h"}' \
    ttl=1h max_ttl=24h

# 6. Issue a dynamic virtual key. Revoking the lease deletes it in Bifrost.
vault read bifrost/creds/web-app
vault lease revoke <lease_id>
```

Rotate the management token on demand with `vault write bifrost/config/rotate-root management_token=<new>`. Rotation is operator-assisted: Bifrost has no confirmed API to mint management tokens, so you supply the replacement (see [`docs/06-lease-lifecycle.md`](./docs/06-lease-lifecycle.md)).

## Install from a release

Releases publish static `linux/amd64` and `linux/arm64` binaries with a `SHA256SUMS` file, an SPDX SBOM per binary, and a SLSA build provenance attestation. There is no container image - Vault `exec`s a native binary from `plugin_directory` and never pulls one.

```sh
TAG=v0.1.0
REPO=globallogicuki/vault-plugin-secrets-bifrost
ARCH=amd64                      # or arm64
PLUGIN_DIR=/vault/plugins       # must match plugin_directory in Vault's config
BIN=vault-plugin-secrets-bifrost_${TAG#v}_linux_$ARCH
BASE=https://github.com/$REPO/releases/download/$TAG

# 1. Download the binary and the checksum file.
curl -fsSLO "$BASE/$BIN"
curl -fsSLO "$BASE/SHA256SUMS"

# 2. Verify integrity. SHA256SUMS covers both architectures.
sha256sum --ignore-missing -c SHA256SUMS

# 3. Optional: verify it was built by this repository's release workflow.
gh attestation verify "$BIN" -R "$REPO"

# 4. Install into Vault's plugin_directory and register it. Vault re-checks
#    this SHA256 on every plugin launch, so take it from SHA256SUMS.
install -m 0755 "$BIN" "$PLUGIN_DIR/vault-plugin-secrets-bifrost"
vault plugin register \
    -sha256="$(awk -v a="$ARCH" '$2 ~ "_linux_"a"$" {print $1}' SHA256SUMS)" \
    -version="$TAG" \
    secret vault-plugin-secrets-bifrost
vault secrets enable -path=bifrost -plugin-version="$TAG" vault-plugin-secrets-bifrost
```

Note the two spellings of the version: the tag and Vault's plugin catalogue use `v0.1.0`, asset filenames use `0.1.0`.

## Building

```sh
make build      # compile the plugin into vault/plugins/ for this host
make test       # unit + backend tests against a mock Bifrost
make test-ci    # what CI runs: race detector + coverage
make testacc    # acceptance tests (needs BIFROST_ADDR + BIFROST_MANAGEMENT_TOKEN)
make lint       # gofmt check + go vet, no extra tooling needed
make lint-all   # the above, plus golangci-lint
make checksums  # cross-compile linux/amd64 + arm64 into dist/, with SHA256SUMS
```

The Go module path is `github.com/globallogicuki/vault-plugin-secrets-bifrost`.

Deployment is split into two independent streams, coupled only by the artefact contract restated in both:

- [`docs/12-build-and-release.md`](./docs/12-build-and-release.md) - building and publishing the plugin artefact.
- [`docs/11-kubernetes-deployment.md`](./docs/11-kubernetes-deployment.md) - pulling that artefact into a Vault cluster on Kubernetes.

## Licence

Released under the [Mozilla Public License 2.0](./LICENSE).
