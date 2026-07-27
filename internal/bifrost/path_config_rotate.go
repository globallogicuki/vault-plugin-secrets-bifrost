package bifrost

import (
	"context"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

// pathConfigRotateRoot backs config/rotate-root.
//
// Rotation is operator-assisted. Bifrost is not known to expose an API for
// programmatically minting management tokens (docs/04, docs/09 open question 1),
// so rotation swaps in an operator-supplied replacement token rather than
// minting one. The same routine backs the scheduled rotation callback
// (RotateCredential): on a scheduled tick with no supplied token it records a
// warning rather than inventing a token.
func (b *backend) pathConfigRotateRoot(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	newToken := ""
	if v, ok := d.GetOk("management_token"); ok {
		newToken = v.(string)
	}
	if newToken == "" {
		return logical.ErrorResponse(
			"management_token is required: Bifrost has no confirmed API to mint management tokens, " +
				"so rotation is operator-assisted. Create a replacement token in Bifrost and supply it here.",
		), nil
	}

	if err := b.swapManagementToken(ctx, req.Storage, newToken); err != nil {
		return nil, err
	}
	return nil, nil
}

// rotateRootCredential is the RotateCredential callback invoked by Vault's
// rotation manager on the configured schedule. Because rotation is
// operator-assisted, a scheduled tick cannot mint a token itself; it logs a
// warning so operators know a rotation is due. When Bifrost gains a token-mint
// endpoint, this is where automatic minting would hook in (docs/06).
func (b *backend) rotateRootCredential(ctx context.Context, req *logical.Request) error {
	b.Logger().Warn("scheduled management-token rotation is due but Bifrost has no " +
		"programmatic token-mint API; supply a replacement via config/rotate-root " +
		"(operator-assisted rotation)")
	return nil
}

// swapManagementToken persists a replacement management token and refreshes the
// cached client. It is guarded by rotateLock so an on-demand rotation cannot
// race the scheduled callback. Persist-then-swap: the new token is only made
// live after it is safely stored (docs/06 rotation failure handling).
func (b *backend) swapManagementToken(ctx context.Context, s logical.Storage, newToken string) error {
	b.rotateLock.Lock()
	defer b.rotateLock.Unlock()

	cfg, err := getConfig(ctx, s)
	if err != nil {
		return err
	}
	if cfg == nil {
		return errBackendNotConfigured
	}

	cfg.ManagementToken = newToken
	if err := putConfig(ctx, s, cfg); err != nil {
		return err
	}
	// Force the next call to rebuild the client with the new token.
	b.clearClient()
	return nil
}

func pathConfigRotate(b *backend) []*framework.Path {
	return []*framework.Path{
		{
			Pattern: "config/rotate-root$",
			Fields: map[string]*framework.FieldSchema{
				"management_token": {
					Type:        framework.TypeString,
					Description: "Operator-supplied replacement Bifrost management token to swap in.",
					DisplayAttrs: &framework.DisplayAttributes{
						Sensitive: true,
					},
				},
			},
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.UpdateOperation: &framework.PathOperation{Callback: b.pathConfigRotateRoot},
			},
			HelpSynopsis: "Rotate the Bifrost management token on demand (operator-assisted).",
			HelpDescription: "Swaps in an operator-supplied replacement management token and refreshes the cached client. " +
				"Bifrost has no confirmed API to mint management tokens, so the replacement must be created in Bifrost and supplied here. " +
				"Already-issued virtual keys are unaffected.",
		},
	}
}
