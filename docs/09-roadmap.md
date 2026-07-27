# 09 - Roadmap

Phased delivery. Each phase is independently useful and leaves the plugin in a shippable state.

## Phase 0 - Design (current)

- [x] Repo, README, licence (MPL-2.0).
- [x] This design framework (`docs/`).
- [ ] Resolve open questions (below) and confirm module path.

## Phase 1 - MVP: dynamic virtual keys

Goal: `vault read bifrost/creds/<role>` issues and auto-revokes a Bifrost virtual key.

- Backend skeleton (`Factory`, `framework.Backend`, plugin `main`).
- `config` + `config/rotate-root`, including **automated root rotation** (`rotation_period` / `rotation_schedule`) via Vault's rotation manager. Runs fully automatically if Bifrost can provision tokens programmatically; operator-assisted otherwise.
- `roles/<name>` CRUD + list with validation.
- `creds/<role>` read → create VK; `bifrost_vk` secret with `Revoke`/`Renew`.
- Bifrost client (create/get/update/delete VK) with error/idempotency handling.
- WAL rollback for orphan prevention.
- Unit + backend (mock Bifrost) tests; dev harness + Makefile.

**Exit criteria:** end-to-end issue → use → lease-revoke → key-invalid, verified against a real Bifrost.

## Phase 2 - Provider API keys

Goal: manage provider-level keys (`/api/providers/{provider}/keys`).

- `provider-keys/config` and provider-key roles.
- Handle **redacted `value` on response** (capture at create; store in lease response only).
- Delete-on-revoke; same WAL pattern.
- Decide whether provider keys are dynamic (create per lease) or predominantly **static** (see phase 3).

## Phase 3 - Static roles & rotation

Goal: manage the lifecycle of long-lived, shared credentials Vault didn't originally mint.

- `static-roles/<name>` + `static-creds/<name>` (Vault reads the current value; rotation on a schedule).
- Rotation via create-new → swap → delete-old (no native rotate endpoint; see [04](./04-bifrost-integration.md)).
- Manual rotate trigger (`rotate-role`-style path).

## Phase 4 - Enterprise / scale

- Vault **namespaces** and **HCP Vault** compatibility.
- Bifrost **clustering** awareness (management calls + `POST /api/vault/flush-cache` semantics if relevant).
- Performance-standby correctness (`Invalidate`, forwarding of writes).
- Observability: metrics on issue/revoke rates, Bifrost call latency/errors.

## Open questions to resolve before/into Phase 1

| # | Question | Owner | Blocks |
|---|----------|-------|--------|
| 1 | Can Bifrost's management API programmatically **create and revoke management tokens**? Required for fully **automatic** root rotation; without it, rotation is operator-assisted. | - | automatic root rotation (Phase 1) |
| 2 | Exact **pinned Bifrost API version** and any request-schema drift vs. the docs. | - | client implementation |
| 3 | `go.mod` **module path** (`github.com/<org>/vault-plugin-secrets-bifrost`). | - | repo init of Go module |
| 4 | Are **scoped** management tokens available (VK-only) for least privilege? | - | security posture |
| 5 | Should MVP set `expires_at` backstop by default? (design says yes) | - | role defaults |

## Non-goals recap

Re-implementing Bifrost's native `vault.<path>` **consumption** backend, RBAC/user management, and a Vault **auth method** are explicitly out of scope (see [01](./01-overview.md)).
