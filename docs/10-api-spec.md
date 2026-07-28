# 10 - Plugin API specification

The HTTP/CLI API the plugin exposes **to Vault clients**. This is the Vault-facing contract (operators and applications calling Vault); the Bifrost-facing contract the engine consumes is in [04](./04-bifrost-integration.md). Path design and rationale are in [05](./05-secret-engine-paths.md); this document pins the exact fields, verbs, status codes, and payloads as implemented in Phase 1.

- **Mount path** is operator-chosen; examples use `bifrost/`.
- **Base URL** for HTTP calls is `${VAULT_ADDR}/v1/`.
- **Auth**: every request carries a Vault token (`X-Vault-Token` header, or `Authorization: Bearer`), gated by ACL policy - see [Access control](#access-control).
- Vault wraps every response in its standard envelope; only the engine-specific `data` (and, for issue, `lease_*`) fields are shown below.

## Endpoint summary

| Path | Verbs | Purpose |
|------|-------|---------|
| `config` | `POST`, `GET` | Bifrost connection + management token + rotation settings |
| `config/rotate-root` | `POST` | Swap in a new management token (operator-assisted) |
| `roles` | `LIST` | List role names |
| `roles/<name>` | `POST`, `GET`, `DELETE` | Manage a virtual-key role (scope + TTL template) |
| `creds/<role>` | `GET` | Issue a dynamic, leased virtual key |

CLI verb mapping: `vault write` -> `POST`, `vault read` -> `GET`, `vault list` -> `LIST`, `vault delete` -> `DELETE`.

---

## `config`

Connection and credentials for one mount. Uses Vault's standard `automated_rotation` fields, so behaviour matches the `database`/`aws` engines.

### `POST /v1/bifrost/config`

Create or update the config. On create, `address` and `management_token` are required; on update, omitted fields keep their stored value.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `address` | string | yes | - | Base URL of the Bifrost management API, e.g. `https://bifrost.internal:8080` |
| `management_token` | string | yes | - | Bearer token for `/api/*`. **Write-only**; never read back |
| `request_timeout` | duration (s) | no | `30` | Per-request timeout for calls to Bifrost |
| `tls_ca_cert` | string | no | - | PEM CA bundle used to verify the Bifrost certificate |
| `tls_skip_verify` | bool | no | `false` | Disable TLS verification. Development only; unsafe |
| `api_version` | string | no | - | Optional Bifrost API version to record/pin |
| `rotation_period` | duration | no | `0` (off) | Rotate the management token every N (e.g. `720h`). Mutually exclusive with `rotation_schedule` |
| `rotation_schedule` | string (cron) | no | - | Cron schedule for rotation (e.g. `0 0 1 * *`). Mutually exclusive with `rotation_period` |
| `rotation_window` | duration | no | - | With `rotation_schedule`, the window after each tick during which rotation may run |
| `disable_automated_rotation` | bool | no | `false` | Suspend scheduled rotation without clearing the period/schedule |

**Response:** `204 No Content` (or `200` with a warning if a rotation schedule was supplied but this Vault edition has no rotation manager - the schedule is saved but will not run; use `config/rotate-root` to rotate on demand).

```sh
vault write bifrost/config \
    address=https://bifrost.internal:8080 \
    management_token=$BIFROST_MANAGEMENT_TOKEN \
    request_timeout=30s
```

### `GET /v1/bifrost/config`

Read the config. **`management_token` is deliberately omitted** - it is never returned (see [07](./07-security.md)).

```json
{
  "data": {
    "address": "https://bifrost.internal:8080",
    "request_timeout": 30,
    "tls_ca_cert": "",
    "tls_skip_verify": false,
    "api_version": "",
    "rotation_period": 0,
    "rotation_schedule": "",
    "rotation_window": 0,
    "disable_automated_rotation": false
  }
}
```

**Errors:** `404` if no config has been written.

---

## `config/rotate-root`

### `POST /v1/bifrost/config/rotate-root`

Immediately swap the stored management token for an operator-supplied replacement, independent of any schedule. Persist-then-swap: the new token is stored (Vault-encrypted) before it is made live, then the cached client is rebuilt.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `management_token` | string | yes | Replacement Bifrost management token to swap in |

**Response:** `204 No Content`.

> **Operator-assisted only.** Bifrost has no confirmed API to mint management tokens programmatically ([04](./04-bifrost-integration.md), [09](./09-roadmap.md)). Calling this without `management_token` returns `400` with guidance to create a replacement in Bifrost first. A scheduled rotation tick cannot mint a token either; it logs a warning that rotation is due.

```sh
vault write bifrost/config/rotate-root management_token=$NEW_TOKEN
```

---

## `roles`

### `LIST /v1/bifrost/roles`

List role names. (`GET .../roles?list=true`, or `vault list bifrost/roles`.)

```json
{ "data": { "keys": ["web-app", "batch-worker"] } }
```

---

## `roles/<name>`

A template for the virtual keys `creds/<name>` issues.

### `POST /v1/bifrost/roles/<name>`

| Field | Type | Required | Default | Maps to (Bifrost create-VK) |
|-------|------|----------|---------|-----------------------------|
| `provider_configs` | list(object) | yes | - | `provider_configs` (provider, weight, allowed_models, blacklisted_models, budgets, rate_limit) |
| `budgets` | list(object) | no | - | key-level `budgets`, each with `max_limit` + `reset_duration` |
| `rate_limit` | object | no | - | key-level `rate_limit` |
| `is_active` | bool | no | `true` | `is_active` |
| `ttl` | duration (s) | no | mount default | lease TTL; clamped to the mount default |
| `max_ttl` | duration (s) | no | mount max | lease max TTL; clamped to the mount max |
| `name_template` | string | no | `vault-{{display_name}}-{{role}}-{{random}}` | generates the VK `name` (see [Name templating](#name-templating)) |
| `set_expires_at` | bool | no | `true` | if true, set the VK `expires_at` to about `now+ttl` as a Bifrost-side backstop |

**Validation** (returns `400` on failure):

- At least one `provider_configs` entry (Bifrost is deny-by-default with none).
- `ttl <= max_ttl`.
- Budget entries require `max_limit` + `reset_duration`.

**Response:** `204 No Content`.

> **Passing object/list fields.** `rate_limit` (a map) is not coerced from an inline `key='{...}'` string by the Vault CLI. Supply the whole body as JSON on stdin, which uses the same code path as the HTTP API and Terraform:
>
> ```sh
> vault write bifrost/roles/web-app - <<'EOF'
> {
>   "provider_configs": [{"provider":"openai","allowed_models":["gpt-4o"],"weight":1}],
>   "rate_limit": {"request_max_limit":1000,"request_reset_duration":"1h"},
>   "ttl": "1h",
>   "max_ttl": "24h"
> }
> EOF
> ```

### `GET /v1/bifrost/roles/<name>`

```json
{
  "data": {
    "provider_configs": [{"provider": "openai", "allowed_models": ["gpt-4o"], "weight": 1}],
    "budgets": null,
    "rate_limit": {"request_max_limit": 1000, "request_reset_duration": "1h"},
    "is_active": true,
    "ttl": 3600,
    "max_ttl": 86400,
    "name_template": "vault-{{display_name}}-{{role}}-{{random}}",
    "set_expires_at": true
  }
}
```

**Errors:** `404` if the role does not exist.

### `DELETE /v1/bifrost/roles/<name>`

Removes the role. Does **not** revoke virtual keys already issued from it - those are tied to their own leases. **Response:** `204 No Content` (idempotent; deleting an absent role also succeeds).

---

## `creds/<role>`

### `GET /v1/bifrost/creds/<role>`

Issue a dynamic virtual key from the named role. Each read produces a fresh key with a unique name and its own lease.

Flow: load role + config -> generate a unique name -> record a WAL entry -> `POST /api/governance/virtual-keys` -> frame a leased `bifrost_vk` secret -> clear the WAL. (WAL rollback deletes an orphaned key if a crash lands between create and lease-persist; see [06](./06-lease-lifecycle.md).)

**Response** (`200`): the lease envelope plus:

| Field | Location | Description |
|-------|----------|-------------|
| `value` | `data` | The `sk-bf-…` secret. Returned **once**; not stored by the plugin |
| `vk_id` | `data` | Bifrost virtual-key id (also held in lease `InternalData` for revoke/renew) |
| `lease_id` | envelope | Vault lease id; revoke this to delete the key |
| `lease_duration` | envelope | TTL in seconds (from the role) |
| `renewable` | envelope | `true` |

```json
{
  "lease_id": "bifrost/creds/web-app/uFdDGmrBs7tK7KvkZHvVV8yi",
  "lease_duration": 3600,
  "renewable": true,
  "data": {
    "value": "sk-bf-ac2b33e3-1cc8-4473-ac29-ea838dbc3bdc",
    "vk_id": "ed0dd8d4-2a1e-452c-961f-959924bd72d0"
  }
}
```

**Errors:** `404`/`400` if the role is unknown; `400` if the backend is not configured; `5xx` if Bifrost rejects the create (the plugin surfaces Bifrost's error verbatim, e.g. `invalid provider name: openai` when the provider is not registered in Bifrost - see [08](./08-testing-and-dev.md)).

### Lease operations

The issued credential is a `bifrost_vk` secret. Vault drives its lifecycle via the standard lease endpoints; the plugin implements the callbacks.

| Vault action | Endpoint | Plugin behaviour |
|--------------|----------|------------------|
| Renew | `PUT /v1/sys/leases/renew` (`vault lease renew`) | Extend the lease up to `max_ttl`; if `set_expires_at`, push the VK `expires_at` forward in Bifrost |
| Revoke | `PUT /v1/sys/leases/revoke` (`vault lease revoke`) | `DELETE /api/governance/virtual-keys/{vk_id}`; a Bifrost `404` is treated as success (idempotent); a `5xx`/network error is returned as retryable so Vault retries |

```sh
vault read  bifrost/creds/web-app
vault lease renew  <lease_id>
vault lease revoke <lease_id>
```

---

## Name templating

`name_template` builds the Bifrost virtual-key `name`. Placeholders:

| Placeholder | Expands to |
|-------------|------------|
| `{{role}}` | The role name |
| `{{display_name}}` | The calling token/entity's display name (e.g. `userpass-alice`, `token` for the root token), sanitised to `[A-Za-z0-9_-]`; an empty/stripped value becomes `unknown` |
| `{{random}}` | A 12-character random suffix, ensuring uniqueness per issue |

The default `vault-{{display_name}}-{{role}}-{{random}}` makes each key traceable to its requester and role, mirroring the `aws`/`database` engines. Example issued names: `vault-token-web-app-Mw9TfnaBHxgt`, `vault-userpass-alice-web-app-4FPOc0j3FFKH`.

---

## Error model

Vault-level failures use standard HTTP status codes with `{"errors": ["..."]}`. Common cases:

| Status | Meaning |
|--------|---------|
| `400` | Validation failure (missing required field, `ttl > max_ttl`, no `provider_configs`, rotate-root with no token) |
| `403` | Vault token lacks the required capability on the path |
| `404` | Config or role not found |
| `500` | Downstream Bifrost error (auth failure, provider rejection, `5xx`) surfaced to the caller |

Secrets are redacted from all error messages: the management token and any `sk-bf-…` value are masked before an error can reach a response or log ([07](./07-security.md)).

---

## Access control

Least-privilege split - applications read credentials only; operators manage config and roles.

Application policy:

```hcl
# Apps may only issue credentials.
path "bifrost/creds/*" {
  capabilities = ["read"]
}
```

Operator policy:

```hcl
path "bifrost/config" {
  capabilities = ["create", "update", "read"]
}
path "bifrost/config/rotate-root" {
  capabilities = ["update"]
}
path "bifrost/roles/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}
```

| Path | Capabilities | Typical grantee |
|------|--------------|-----------------|
| `creds/*` | `read` | Applications |
| `config`, `config/rotate-root` | `create`/`update`/`read` | Operators |
| `roles/*` | full CRUD + `list` | Operators |

> Cross-references: path rationale [05](./05-secret-engine-paths.md); Bifrost API the engine consumes [04](./04-bifrost-integration.md); lease/rotation lifecycle [06](./06-lease-lifecycle.md); secrets handling and audit [07](./07-security.md).
