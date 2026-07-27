package bifrost

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

const secretTypeVK = "bifrost_vk"

// secretVK defines the dynamic virtual key as a Vault secret so the lease
// manager can renew and revoke it (docs/03).
func secretVK(b *backend) *framework.Secret {
	return &framework.Secret{
		Type: secretTypeVK,
		Fields: map[string]*framework.FieldSchema{
			"value": {
				Type:        framework.TypeString,
				Description: "The Bifrost virtual key (sk-bf-...).",
			},
			"vk_id": {
				Type:        framework.TypeString,
				Description: "The Bifrost virtual key id.",
			},
		},
		Renew:  b.secretVKRenew,
		Revoke: b.secretVKRevoke,
	}
}

// secretVKRevoke deletes the virtual key in Bifrost when its lease ends. A 404
// is treated as success (idempotent); transient failures return a retryable
// error so Vault retries revocation (docs/06).
func (b *backend) secretVKRevoke(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	vkID, err := internalString(req.Secret.InternalData, "vk_id")
	if err != nil {
		return nil, err
	}

	client, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, err
	}

	if err := client.DeleteVirtualKey(ctx, vkID); err != nil && !isNotFound(err) {
		// Retryable errors propagate so Vault retries; the Bifrost-side
		// expires_at backstop also protects if revocation is delayed.
		return nil, err
	}
	return nil, nil
}

// secretVKRenew extends the lease up to the role's max_ttl. The same vk_id is
// kept for the whole lease; if set_expires_at is on, the key's Bifrost
// expires_at is pushed forward so the backstop stays ahead of the lease.
func (b *backend) secretVKRenew(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	roleName, err := internalString(req.Secret.InternalData, "role")
	if err != nil {
		return nil, err
	}

	role, err := getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if role == nil {
		// Role deleted since issue: let the lease run to its current end.
		return nil, nil
	}

	resp := &logical.Response{Secret: req.Secret}
	resp.Secret.TTL = role.TTL
	resp.Secret.MaxTTL = role.MaxTTL

	if role.SetExpiresAt {
		vkID, err := internalString(req.Secret.InternalData, "vk_id")
		if err != nil {
			return nil, err
		}
		client, err := b.getClient(ctx, req.Storage)
		if err != nil {
			return nil, err
		}
		// Best-effort: push expires_at to roughly the new lease end. A
		// failure here should not fail the renewal itself.
		newExpiry := time.Now().UTC().Add(role.MaxTTL)
		if role.MaxTTL == 0 {
			newExpiry = time.Now().UTC().Add(role.TTL)
		}
		if err := client.UpdateVirtualKey(ctx, vkID, map[string]interface{}{
			"expires_at": newExpiry.Format(time.RFC3339),
		}); err != nil && !isNotFound(err) {
			b.Logger().Warn("failed to sync virtual key expires_at on renew", "vk_id", vkID, "error", redactSecrets(err.Error(), ""))
		}
	}

	return resp, nil
}

// internalString reads a required string from a secret's InternalData.
func internalString(data map[string]interface{}, key string) (string, error) {
	v, ok := data[key]
	if !ok {
		return "", errors.New("bifrost: missing " + key + " in lease internal data")
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New("bifrost: " + key + " in lease internal data is not a string")
	}
	return s, nil
}
