# 02 - Architecture

## Components

```mermaid
flowchart TD
    subgraph vault["Vault server"]
        subgraph plugin["vault-plugin-secrets-bifrost (framework.Backend)"]
            paths["Paths<br/>• config<br/>• config/rotate-root<br/>• roles/&lt;name&gt;<br/>• creds/&lt;role&gt;"]
            client["Bifrost API client"]
            secret["Secret type: bifrost_vk<br/>• Response (issue)<br/>• Revoke (delete VK)<br/>• Renew (extend lease)"]
            paths -->|creds read| client
            client --> secret
        end
        storage[("Storage (Vault-encrypted)<br/>config/ (1 entry)<br/>role/&lt;name&gt; (N entries)")]
        paths <--> storage
    end
    api["Bifrost management API<br/>/api/governance/virtual-keys"]
    client -->|"HTTPS, Authorization: Bearer &lt;management token&gt;"| api
```

- **Backend** - a `framework.Backend` from the Vault SDK; owns path routing, storage, and the secret type.
- **Bifrost API client** - a thin Go HTTP client over the management API (see [04](./04-bifrost-integration.md)). Reads its base URL and bearer token from the `config` storage entry.
- **Storage** - Vault-encrypted backend storage holds the `config` entry and role definitions. It does **not** persist issued virtual key values; those live only in the lease's response and in Bifrost.
- **Secret type `bifrost_vk`** - defines the `Revoke` and `Renew` behaviour that Vault's lease manager invokes.

## Data flow - issue

```mermaid
sequenceDiagram
    participant App as Application
    participant Creds as creds/:role path
    participant Client as Bifrost client
    participant Bifrost as Bifrost management API

    App->>Creds: vault read bifrost/creds/web-app
    Creds->>Creds: load role "web-app" + config
    Creds->>Client: build create-VK request
    Client->>Bifrost: POST /api/governance/virtual-keys<br/>(providers, budgets, rate_limit, expires_at)
    Bifrost-->>Client: 200 { virtual_key: { id, value: "sk-bf-…" } }
    Client-->>Creds: id + value
    Creds->>Creds: frame Secret{ value, vk_id }<br/>attach lease (TTL/max-TTL from role)
    Creds-->>App: { value: "sk-bf-…", lease_id, lease_duration }
```

## Data flow - renew

```mermaid
flowchart TD
    start["Lease nearing expiry<br/>(secret renew callback)"]
    check{"Within role<br/>max-TTL?"}
    expire["Let lease expire → revoke"]
    patch["Optionally PATCH the VK's expires_at<br/>in Bifrost to match new lease end"]
    extend["Extend Vault lease"]

    start --> check
    check -->|no| expire
    check -->|yes| patch --> extend
```

## Data flow - revoke

```mermaid
sequenceDiagram
    participant Vault as Vault lease manager
    participant Revoke as secret revoke callback
    participant Client as Bifrost client
    participant Bifrost as Bifrost management API

    Vault->>Revoke: lease expiry / vault lease revoke
    Revoke->>Revoke: read vk_id from internal data
    Revoke->>Client: delete vk_id
    Client->>Bifrost: DELETE /api/governance/virtual-keys/:vk_id
    Bifrost-->>Client: 200 / 404 (already gone → success, idempotent)
    Client-->>Revoke: ok
    Revoke->>Vault: lease removed
```

## Where secrets live

| Secret | Location | Notes |
|--------|----------|-------|
| Bifrost management (bearer) token | `config` storage entry, Vault-encrypted | Never returned to callers; rotatable via `config/rotate-root` |
| Issued virtual key `value` (`sk-bf-…`) | Returned in lease response; held in Bifrost | Not persisted in plugin storage; if lost, revoke + re-issue |
| Virtual key `id` | Internal `InternalData` of the lease's secret | Needed at revoke time |

## Reliability considerations

- **Partial failure on issue.** If Bifrost creates the VK but Vault fails to record the lease, the VK could leak. Mitigation: use a **WAL** (write-ahead log) entry written *before* the Bifrost call, rolled back (VK deleted) if the operation doesn't complete. Detailed in [03](./03-vault-plugin-interface.md) and [06](./06-lease-lifecycle.md).
- **Idempotent revoke.** A `404` from Bifrost on delete means the key is already gone - treat as success so leases can always be cleaned up.
- **Bifrost unavailable at revoke.** Vault retries revocation on its own schedule; the revoke callback should return a retryable error rather than dropping the lease.
