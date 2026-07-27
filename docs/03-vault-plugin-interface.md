# 03 - Vault plugin interface

How the plugin sits on the [Vault plugin SDK](https://github.com/hashicorp/vault/tree/main/sdk). This is a **secrets** plugin (not auth), built as a standalone binary that Vault runs over its plugin RPC.

## Entry point

```go
// cmd/vault-plugin-secrets-bifrost/main.go
func main() {
    apiClientMeta := &pluginutil.APIClientMeta{}
    flags := apiClientMeta.FlagSet()
    flags.Parse(os.Args[1:])

    tlsConfig := apiClientMeta.GetTLSConfig()
    tlsProviderFunc := api.VaultPluginTLSProvider(tlsConfig)

    plugin.ServeMultiplex(&plugin.ServeOpts{
        BackendFactoryFunc: bifrost.Factory,   // our backend
        TLSProviderFunc:    tlsProviderFunc,
    })
}
```

## Backend

The backend is a `*framework.Backend` created by a `Factory`:

```go
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
    b := newBackend()
    if err := b.Setup(ctx, conf); err != nil {
        return nil, err
    }
    return b, nil
}

type backend struct {
    *framework.Backend
    client     *bifrostClient  // lazily built from config
    clientLock sync.RWMutex
}
```

`framework.Backend` wires together:

- `Help` - mount help text.
- `BackendType: logical.TypeLogical` - a secrets engine.
- `Paths` - the path definitions (see [05](./05-secret-engine-paths.md)).
- `Secrets` - the `bifrost_vk` secret type.
- `PathsSpecial` - mark `config` (and `config/rotate-root`) as sensitive.
- `WALRollback` / `WALRollbackMinAge` - orphan cleanup.
- `RotateCredential` (+ the `automated_rotation` fields on `config`) - registers the management token with Vault's rotation manager for scheduled root rotation.
- `Invalidate` - drop the cached client when `config` changes (important for clustered/perf-standby nodes).

### Automated root rotation

The management token is registered with Vault's rotation manager so it rotates on the operator's schedule ([05](./05-secret-engine-paths.md), [06](./06-lease-lifecycle.md)):

- `config`'s field schema includes the SDK helper `automatedrotationutil.AutomationRotationParams` (`rotation_period`, `rotation_schedule`, `rotation_window`, `disable_automated_rotation`).
- On `config` write, the backend calls `b.System().RegisterRotationJob(ctx, &rotation.RotationJobConfigureRequest{...})`; clearing the settings deregisters it.
- The scheduler invokes the backend's rotation callback, which runs the same `rotateRootCredential` routine as the manual `config/rotate-root` path - so both share one implementation.

```go
func (b *backend) rotateRootCredential(ctx context.Context, req *logical.Request) error {
    // 1. provision a new management token in Bifrost using the current one
    // 2. persist it to the config storage entry (Vault-encrypted)
    // 3. revoke/discard the previous token
    // 4. clear the cached client so the next call uses the new token
}
```

## Path definitions

Each path is a `*framework.Path` with a `Pattern`, `Fields`, and per-operation callbacks. Sketch:

```go
func (b *backend) paths() []*framework.Path {
    return framework.PathAppend(
        pathConfig(b),        // config, config/rotate-root
        pathRoles(b),         // roles/<name>  (CRUD + list)
        []*framework.Path{ pathCredentials(b) }, // creds/<role>
    )
}
```

Operation callbacks use the typed signature:

```go
func (b *backend) pathCredsRead(ctx context.Context, req *logical.Request,
    d *framework.FieldData) (*logical.Response, error)
```

## Secret type

The dynamic virtual key is modelled as a Vault secret so the lease manager can renew/revoke it:

```go
const secretTypeVK = "bifrost_vk"

func secretVK(b *backend) *framework.Secret {
    return &framework.Secret{
        Type: secretTypeVK,
        Fields: map[string]*framework.FieldSchema{
            "value": {Type: framework.TypeString, Description: "Bifrost virtual key (sk-bf-…)"},
        },
        Renew:  b.secretVKRenew,
        Revoke: b.secretVKRevoke,
    }
}
```

`pathCredsRead` frames the response with `b.Secret(secretTypeVK).Response(data, internal)` where:

- `data` - returned to the caller: `{ "value": "sk-bf-…", "vk_id": "vk_123" }`.
- `internal` - kept by Vault, given back to `Revoke`/`Renew`: `{ "vk_id": "vk_123", "role": "web-app" }`.

The response's `Secret.TTL` / `Secret.MaxTTL` are set from the role.

## Lease callbacks

```go
func (b *backend) secretVKRevoke(ctx context.Context, req *logical.Request,
    d *framework.FieldData) (*logical.Response, error) {
    vkID := req.Secret.InternalData["vk_id"].(string)
    client, err := b.getClient(ctx, req.Storage)
    if err != nil { return nil, err }
    if err := client.DeleteVirtualKey(ctx, vkID); err != nil && !isNotFound(err) {
        return nil, err   // retryable: Vault will retry revocation
    }
    return nil, nil
}
```

`secretVKRenew` checks the role's max-TTL and calls `framework.LeaseExtend`-style logic, optionally syncing the VK's `expires_at` in Bifrost.

## Write-ahead logs (WAL)

To avoid orphaned virtual keys when a crash happens between "Bifrost created the VK" and "Vault recorded the lease":

1. In `pathCredsRead`, before calling Bifrost, `PutWAL` a record `{ role }`.
2. After a successful issue, include the returned `vk_id`; on full success the WAL is deleted.
3. `WALRollback` (invoked periodically by Vault, respecting `WALRollbackMinAge`) finds stale WALs and deletes the corresponding VK in Bifrost.

## Registration & lifecycle (operator view)

```sh
# build (Linux/macOS only; must match Vault's Go/deps for in-process constraints)
go build -o vault/plugins/vault-plugin-secrets-bifrost ./cmd/vault-plugin-secrets-bifrost

# register (Vault computes/pins the sha256)
vault plugin register -sha256=$(sha256sum vault/plugins/vault-plugin-secrets-bifrost | cut -d' ' -f1) \
    secret vault-plugin-secrets-bifrost

# enable at a mount
vault secrets enable -path=bifrost vault-plugin-secrets-bifrost
```

## SDK dependencies

- `github.com/hashicorp/vault/sdk` - `framework`, `logical`, `plugin`.
- `github.com/hashicorp/vault/api` - TLS provider for the plugin server.
- Standard library `net/http` for the Bifrost client (kept dependency-light).

> Cross-references: path details in [05](./05-secret-engine-paths.md); lease/TTL semantics in [06](./06-lease-lifecycle.md); the Bifrost client in [04](./04-bifrost-integration.md).
