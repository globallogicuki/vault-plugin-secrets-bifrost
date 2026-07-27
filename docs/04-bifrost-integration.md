# 04 - Bifrost integration

The plugin is a client of Bifrost's **management API**. This document pins the endpoints, auth, and behaviours the engine relies on. All facts here come from [docs.getbifrost.ai](https://docs.getbifrost.ai) as of the design date; **verify against the running Bifrost version before implementation** and record the pinned version below.

> **Pinned Bifrost version:** _TODO - fill in at implementation time (e.g. v1.x). The API is versioned under `/api`._

## Authentication

- All `/api/*` management endpoints require **`Authorization: Bearer <management token>`** (`ManagementBearerAuth`).
- **Virtual keys, dashboard/user/session tokens, and `x-api-key` are NOT accepted** on management endpoints - only the management bearer token.
- The engine stores this token in `config` and never returns it to callers. See [07](./07-security.md).

## Endpoints used

### Virtual keys (MVP)

| Operation | Method & path | Used by |
|-----------|---------------|---------|
| Create | `POST /api/governance/virtual-keys` | `creds/<role>` read → issue |
| Get | `GET /api/governance/virtual-keys/{id}` | verification / renew |
| Update | `PUT /api/governance/virtual-keys/{id}` | renew (sync `expires_at`), `is_active` toggles |
| Delete | `DELETE /api/governance/virtual-keys/{id}` | lease revoke |
| List | `GET /api/governance/virtual-keys` | WAL reconciliation / diagnostics |
| Quota | `GET …/{id}/quota` | optional budget diagnostics |

### Provider keys (phase 2)

| Operation | Method & path |
|-----------|---------------|
| Create | `POST /api/providers/{provider}/keys` |
| Get / List | `GET /api/providers/{provider}/keys[/{id}]` |
| Update | `PUT /api/providers/{provider}/keys/{id}` |
| Delete | `DELETE /api/providers/{provider}/keys/{id}` |

Provider-key `value` is **redacted in responses**, so the engine must capture it at create time (it cannot be re-read). Designed in phase 2; see [09](./09-roadmap.md).

## Create virtual key - request

Built from the role definition ([05](./05-secret-engine-paths.md)). Only `name` is strictly required by Bifrost.

```json
{
  "name": "vault-web-app-<random-suffix>",
  "description": "Issued by Vault (lease <lease_id>)",
  "provider_configs": [
    {
      "provider": "openai",
      "weight": 1,
      "allowed_models": ["gpt-4o", "gpt-4o-mini"],
      "budgets": [{ "max_limit": 100, "reset_duration": "1M" }],
      "rate_limit": { "token_max_limit": 100000, "token_reset_duration": "1d" }
    }
  ],
  "budgets": [{ "max_limit": 500, "reset_duration": "1M" }],
  "rate_limit": { "request_max_limit": 1000, "request_reset_duration": "1h" },
  "is_active": true,
  "expires_at": "2026-07-27T12:34:56Z"
}
```

Notes:

- **`name` uniqueness.** Generate a unique name per issue (role name + random suffix) so concurrent issues don't collide.
- **`expires_at`** is set to the lease's expected end as a defence-in-depth backstop: even if Vault revocation is delayed, Bifrost expires the key. `expires_at` must be in the future.
- **`provider_configs` empty ⇒ deny-by-default** in Bifrost, so a role must specify at least one provider to be useful.

## Create virtual key - response

```json
{
  "message": "Virtual key created successfully",
  "virtual_key": {
    "id": "vk_123",
    "name": "vault-web-app-…",
    "value": "sk-bf-…",
    "is_active": true,
    "created_at": "…", "updated_at": "…"
  }
}
```

The engine captures **`id`** (for later delete/update - stored in lease `InternalData`) and **`value`** (returned once to the caller).

## Rotation strategy (no native rotate endpoint)

Bifrost exposes no "rotate" verb. The engine implements rotation as **create-new → (hand back) → delete-old**:

- **Dynamic secrets** don't need rotation - each lease is a fresh key, and expiry replaces it naturally.
- **Static roles** (phase 3) and **root rotation** implement rotation by creating a replacement credential, updating stored state, then deleting the previous one, so there is no window without a valid credential.

### Management-token (root) rotation - dependency

Automatic and manual root rotation ([05](./05-secret-engine-paths.md), [06](./06-lease-lifecycle.md)) both need Bifrost to **provision and revoke management tokens programmatically**:

| Operation needed | Expected endpoint | Status |
|------------------|-------------------|--------|
| Create a new management token (scoped like the current one) | e.g. `POST /api/api-keys` | **Unconfirmed** - docs describe management keys as dashboard-created only |
| Revoke the previous management token | e.g. `DELETE /api/api-keys/{id}` | **Unconfirmed** |

If these do not exist, rotation runs in **operator-assisted mode**: on each schedule tick (or manual trigger) the engine alerts an operator and/or accepts a supplied replacement token, then performs the persist-and-swap. Confirming or requesting this endpoint on the Bifrost side is a blocking item for fully automatic rotation - tracked in [09](./09-roadmap.md).

## Error & idempotency handling

| Situation | Engine behaviour |
|-----------|------------------|
| `401/403` (bad/expired management token) | Fail the operation with a clear error; surface "check config/rotate-root" |
| `404` on delete | Treat as **success** (already gone) so leases can be cleaned up |
| `409` (name/id conflict on create) | Regenerate suffix and retry a bounded number of times |
| `5xx` / network error on create | Fail read; WAL rollback deletes any partial VK |
| `5xx` / network error on revoke | Return retryable error; Vault retries revocation |

## Client design

- Small `net/http`-based client; base URL + bearer token injected from `config`.
- Context-aware (honours Vault's request context for timeouts/cancellation).
- Configurable request timeout and TLS settings (custom CA, skip-verify for dev) via `config`.
- Redacts the bearer token from all logs and error messages.

> Cross-references: role → request mapping in [05](./05-secret-engine-paths.md); lease/expiry alignment in [06](./06-lease-lifecycle.md).
