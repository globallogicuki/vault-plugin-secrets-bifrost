# Design Framework

This directory holds the design for **`vault-plugin-secrets-bifrost`** - a HashiCorp Vault secrets engine that manages [Bifrost](https://docs.getbifrost.ai/overview) credentials.

The aim is to agree the design here **before** writing plugin code. Each document stands alone but builds on the previous ones.

## Reading order

| # | Document | What it covers |
|---|----------|----------------|
| 1 | [01-overview.md](./01-overview.md) | Problem statement, goals/non-goals, personas, and the critical "which direction" disambiguation |
| 2 | [02-architecture.md](./02-architecture.md) | Components, data flow, and issue/renew/revoke sequences |
| 3 | [03-vault-plugin-interface.md](./03-vault-plugin-interface.md) | How the plugin sits on the Vault SDK (`framework.Backend`, paths, secrets, WALs) |
| 4 | [04-bifrost-integration.md](./04-bifrost-integration.md) | Bifrost management API surface, auth, and the rotation-via-recreate strategy |
| 5 | [05-secret-engine-paths.md](./05-secret-engine-paths.md) | The proposed path layout (`config`, `roles/`, `creds/`, …) |
| 6 | [06-lease-lifecycle.md](./06-lease-lifecycle.md) | TTLs, renewal, revocation, and root-token rotation |
| 7 | [07-security.md](./07-security.md) | Threat model, least privilege, secrets handling, audit |
| 8 | [08-testing-and-dev.md](./08-testing-and-dev.md) | Local dev harness, unit/acceptance tests, CI |
| 9 | [09-roadmap.md](./09-roadmap.md) | Phased delivery plan |
| 10 | [10-api-spec.md](./10-api-spec.md) | Vault-facing API reference: paths, fields, payloads, status codes, ACL |
| 11 | [11-kubernetes-deployment.md](./11-kubernetes-deployment.md) | **Consuming** the artefact: init container, Helm values, registration, upgrades |
| 12 | [12-build-and-release.md](./12-build-and-release.md) | **Producing** the artefact: cross-compilation, checksums, publishing, CI |

Documents 11 and 12 are deliberately separable work streams, coupled only by the artefact contract restated in both.

## Status

Design phase. Nothing here is final until the roadmap MVP scope is signed off.
