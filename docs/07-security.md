# 07 - Security

## Trust model

- **Vault is trusted** to hold the Bifrost management token and to gate access via its own authn/authz + ACL policies.
- **Bifrost management token is highly privileged** - it can create/delete virtual keys across the gateway. Treat it like a database root credential.
- **Applications are semi-trusted** - they authenticate to Vault with their own identity and may read only `creds/<role>` for roles their policy allows. They never see the management token.

## Least privilege

### The management token (engine → Bifrost)

- Scope the token to the minimum management capabilities needed: virtual-key CRUD (and provider-key CRUD in phase 2). Avoid unrelated admin scope if Bifrost supports scoped management tokens.
- Rotate automatically on a user-configurable schedule (`rotation_period` / `rotation_schedule`) and on demand via `config/rotate-root`; never log or return it. See [06](./06-lease-lifecycle.md).
- One token per mount so blast radius is contained and rotation is isolated.

### Vault ACL policies (clients → engine)

```hcl
# application policy - can only obtain leased keys
path "bifrost/creds/web-app" { capabilities = ["read"] }

# operator policy - manages config and roles
path "bifrost/config"        { capabilities = ["create","update"] }
path "bifrost/config/rotate-root" { capabilities = ["update"] }
path "bifrost/roles/*"       { capabilities = ["create","read","update","delete","list"] }
```

Apps must **not** get access to `config` or `roles/*`.

## Secrets handling

| Secret | Protection |
|--------|-----------|
| Management token | Stored only in Vault-encrypted storage; `config` read never returns it; redacted from logs/errors |
| Issued VK `value` | Returned to caller exactly once in the lease response; not persisted by the plugin |
| VK `id` | In lease `InternalData` (Vault-encrypted); not sensitive alone but not exposed beyond what the caller already sees |

- **Response wrapping:** operators should use `-wrap-ttl` when distributing `creds` reads through CI so the `sk-bf-…` value transits as a single-use wrapping token.
- **No secrets in logs:** the Bifrost client and error paths must scrub the bearer token and issued values. Structured errors reference the `vk_id`, never the `value`.

## Transport

- HTTPS to Bifrost enforced by default; `tls_skip_verify` exists for dev only and should be flagged in docs as unsafe.
- Custom CA supported via `config.tls_ca_cert` for private PKI.
- Honour request context deadlines to avoid hanging goroutines.

## Auditability

- Vault's audit log records every `creds` read, `config` write, and lease revoke with the caller's identity - this is the "who got which Bifrost key" trail.
- Bifrost's own audit log (enterprise) records the VK create/delete from the management token's perspective.
- Correlate via the VK `name` (`vault-<role>-<suffix>`) and `description` (embeds the lease id) so a Bifrost-side key maps back to a Vault lease.

## Threat scenarios

| Threat | Mitigation |
|--------|-----------|
| Leaked issued VK | Short TTL + revoke on lease end + Bifrost `expires_at` backstop; operator can `vault lease revoke` immediately |
| Leaked management token | Scheduled auto-rotation shortens exposure; immediate `config/rotate-root`; scope-limited token; audit trail |
| Compromised app identity | Vault ACL limits it to specific `creds/<role>`; budgets/rate limits on the role cap abuse |
| Orphaned keys after crash | WAL rollback ([06](./06-lease-lifecycle.md)) |
| Engine impersonation | Standard Vault plugin sha256 pinning + mTLS plugin RPC |

## Compliance notes

Dynamic, leased, auto-revoked credentials with full audit trails support SOC2/ISO-style controls around credential lifetime and least privilege - a primary motivation for the project ([01](./01-overview.md)).
