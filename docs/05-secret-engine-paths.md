# 05 - Secrets engine paths

The mount path is operator-chosen (examples use `bifrost/`). Layout mirrors the `database`/`aws` engines for familiarity.

```
bifrost/
├── config                 # write/read: address + management token + rotation settings
├── config/rotate-root      # write-only: rotate the management token now (on demand)
├── roles                  # list: role names
├── roles/<name>           # CRUD: a virtual-key template + TTLs
└── creds/<role>           # read: issue a dynamic virtual key (leased)
```

Phase 2/3 additions (designed later): `provider-keys/config`, `static-roles/<name>`, `static-creds/<name>`.

---

## `config`

Stores connection + credentials for Bifrost. One entry per mount.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `address` | string | yes | Base URL of the Bifrost management API, e.g. `https://bifrost.internal:8080` |
| `management_token` | string | yes | Bearer token for `/api/*`. Write-only; never read back |
| `request_timeout` | duration | no (30s) | Per-request timeout |
| `tls_ca_cert` | string | no | PEM CA bundle for verifying Bifrost |
| `tls_skip_verify` | bool | no (false) | Dev only; disables TLS verification |
| `api_version` | string | no | Pin/record the Bifrost API version in use |
| `rotation_period` | duration | no (0 = off) | Automatically rotate the management token every N (e.g. `720h`). Mutually exclusive with `rotation_schedule` |
| `rotation_schedule` | string | no | Cron schedule for rotation (e.g. `0 0 1 * *` = monthly). Mutually exclusive with `rotation_period` |
| `rotation_window` | duration | no | With `rotation_schedule`, the window after each tick during which rotation may run |
| `disable_automated_rotation` | bool | no (false) | Suspend scheduled rotation without clearing the period/schedule |

- `read` returns everything **except** `management_token` (shown as redacted/omitted). It **does** return the rotation settings and the next scheduled rotation time.
- Writing `config` invalidates the cached Bifrost client (`Invalidate`).
- The rotation fields use Vault's standard `automated_rotation` parameters, so behaviour matches the `database`/`aws` engines (see [06](./06-lease-lifecycle.md)).

## `config/rotate-root`

`write` only. Triggers an **immediate, on-demand** rotation of the management token, independent of any schedule. Rotation itself (manual or automatic) runs the same steps:

1. Use the current token to provision a **new** management token in Bifrost.
2. Persist the new token in `config` (Vault-encrypted); it is never returned.
3. Revoke/discard the previous token so no value known outside Vault remains valid.

Automatic rotation is configured on `config` via `rotation_period` or `rotation_schedule` (above); `config/rotate-root` is the manual escape hatch and is also used to seed the first Vault-owned token after enabling the engine.

> **Dependency (open item):** all rotation modes require Bifrost to **provision a replacement management token programmatically**. As of the design date the docs describe management tokens as dashboard-created only, with no create/rotate API. Until such an endpoint is confirmed, rotation degrades to **operator-assisted**: the schedule/manual trigger fires an alert (or accepts an operator-supplied new token) and swaps it in, rather than minting one itself. Tracked in [09](./09-roadmap.md).

## `roles/<name>`

A template for the virtual keys `creds/<name>` will issue. CRUD + list.

| Field | Type | Default | Maps to (Bifrost create-VK) |
|-------|------|---------|-----------------------------|
| `provider_configs` | list(object) | - (required) | `provider_configs` (provider, weight, allowed_models, blacklisted_models, budgets, rate_limit) |
| `budgets` | list(object) | - | key-level `budgets` |
| `rate_limit` | object | - | key-level `rate_limit` |
| `is_active` | bool | true | `is_active` |
| `ttl` | duration | mount default | lease TTL |
| `max_ttl` | duration | mount max | lease max TTL |
| `name_template` | string | `vault-<role>-{{random}}` | generates the VK `name` |
| `set_expires_at` | bool | true | if true, set VK `expires_at ≈ now+ttl` as a backstop |

Validation:

- At least one `provider_configs` entry (Bifrost is deny-by-default with none).
- `ttl <= max_ttl`; both clamped to mount limits.
- Budget entries require `max_limit` + `reset_duration`; rate-limit durations validated.

Example:

```sh
vault write bifrost/roles/web-app \
    provider_configs='[{"provider":"openai","allowed_models":["gpt-4o"],"weight":1}]' \
    rate_limit='{"request_max_limit":1000,"request_reset_duration":"1h"}' \
    ttl=1h max_ttl=24h
```

## `creds/<role>`

`read` only. Issues a dynamic virtual key:

1. Load role + config.
2. Build the create-VK request from the role template (unique `name`, optional `expires_at`).
3. `POST /api/governance/virtual-keys`.
4. Frame a `bifrost_vk` secret with `value` (caller-facing) and `vk_id` (internal), lease TTL from role.

Response:

```
$ vault read bifrost/creds/web-app
Key                Value
---                -----
lease_id           bifrost/creds/web-app/9j2...
lease_duration     1h
lease_renewable    true
value              sk-bf-...
vk_id              vk_123
```

Revoking the lease (`vault lease revoke …`) deletes `vk_123` in Bifrost. See [06](./06-lease-lifecycle.md).

---

## Path capabilities summary

| Path | Create/Update | Read | Delete | List |
|------|:---:|:---:|:---:|:---:|
| `config` | ✓ | ✓ (redacted) | - | - |
| `config/rotate-root` | ✓ | - | - | - |
| `roles/<name>` | ✓ | ✓ | ✓ | ✓ (`roles`) |
| `creds/<role>` | - | ✓ (issues) | - | - |

Vault ACL policies gate these: apps get `read` on `creds/*` only; operators get write on `config`/`roles`.
