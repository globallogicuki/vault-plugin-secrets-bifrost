// Package bifrost implements a HashiCorp Vault secrets-engine backend that
// dynamically issues, leases, and revokes Bifrost virtual keys by calling
// Bifrost's management API. Vault is the control plane; Bifrost is the target.
//
// See docs/ in the repository root for the full design.
package bifrost

import (
	"context"
	"strings"
	"sync"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

// backendHelp is the mount help shown by `vault path-help`.
const backendHelp = `
The Bifrost secrets engine issues dynamic Bifrost virtual keys.

After mounting, write connection details and a management token to "config",
define one or more "roles/<name>" templating the virtual key's scope, then read
"creds/<role>" to obtain a short-lived virtual key bound to a Vault lease. When
the lease expires or is revoked, the virtual key is deleted in Bifrost.
`

// backend is the Bifrost secrets engine. The Bifrost client is built lazily
// from the config storage entry and cached until config changes (Invalidate).
type backend struct {
	*framework.Backend

	clientLock sync.RWMutex
	client     *bifrostClient

	// rotateLock serialises management-token rotation so an on-demand
	// config/rotate-root cannot race the scheduled rotation callback.
	rotateLock sync.Mutex
}

// Factory builds and sets up the backend. Referenced by the plugin's main().
func Factory(ctx context.Context, conf *logical.BackendConfig) (logical.Backend, error) {
	b := newBackend()
	if err := b.Setup(ctx, conf); err != nil {
		return nil, err
	}
	return b, nil
}

func newBackend() *backend {
	b := &backend{}

	b.Backend = &framework.Backend{
		Help:        strings.TrimSpace(backendHelp),
		BackendType: logical.TypeLogical,
		Paths: framework.PathAppend(
			pathConfig(b),
			pathConfigRotate(b),
			pathRoles(b),
			[]*framework.Path{
				pathCredentials(b),
			},
		),
		Secrets: []*framework.Secret{
			secretVK(b),
		},
		PathsSpecial: &logical.Paths{
			// The management token lives under config; keep it off
			// performance-standby nodes and mark it sensitive.
			SealWrapStorage: []string{
				configStoragePath,
			},
		},
		WALRollback:       b.walRollback,
		WALRollbackMinAge: walRollbackMinAge,
		Invalidate:        b.invalidate,
		// Scheduled root rotation shares one routine with the on-demand
		// config/rotate-root path (operator-assisted; see path_config_rotate.go).
		RotateCredential: b.rotateRootCredential,
	}

	return b
}

// invalidate drops the cached client when the config entry changes so the next
// call rebuilds it (important on clustered / performance-standby nodes).
func (b *backend) invalidate(_ context.Context, key string) {
	if key == configStoragePath {
		b.clearClient()
	}
}

// clearClient discards the cached Bifrost client. The next getClient rebuilds
// it from the current config entry.
func (b *backend) clearClient() {
	b.clientLock.Lock()
	defer b.clientLock.Unlock()
	b.client = nil
}

// getClient returns a cached Bifrost client, building it from config on first
// use. Callers must not mutate the returned client.
func (b *backend) getClient(ctx context.Context, s logical.Storage) (*bifrostClient, error) {
	b.clientLock.RLock()
	if b.client != nil {
		defer b.clientLock.RUnlock()
		return b.client, nil
	}
	b.clientLock.RUnlock()

	b.clientLock.Lock()
	defer b.clientLock.Unlock()

	// Re-check: another goroutine may have built it while we waited.
	if b.client != nil {
		return b.client, nil
	}

	cfg, err := getConfig(ctx, s)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, errBackendNotConfigured
	}

	client, err := newBifrostClient(cfg)
	if err != nil {
		return nil, err
	}
	b.client = client
	return client, nil
}
