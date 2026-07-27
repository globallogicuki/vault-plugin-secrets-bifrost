package bifrost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/helper/base62"
	"github.com/hashicorp/vault/sdk/logical"
)

func pathCredentials(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "creds/" + framework.GenericNameRegex("role"),
		Fields: map[string]*framework.FieldSchema{
			"role": {
				Type:        framework.TypeLowerCaseString,
				Description: "Name of the role to issue a virtual key for.",
			},
		},
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ReadOperation: &framework.PathOperation{Callback: b.pathCredsRead},
		},
		HelpSynopsis:    "Issue a dynamic Bifrost virtual key for a role.",
		HelpDescription: "Reading this path creates a virtual key in Bifrost scoped by the named role and binds it to a Vault lease. Revoking the lease deletes the virtual key.",
	}
}

func (b *backend) pathCredsRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	roleName := d.Get("role").(string)

	role, err := getRole(ctx, req.Storage, roleName)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return logical.ErrorResponse("role %q does not exist", roleName), nil
	}

	client, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return nil, err
	}

	// Generate the unique VK name up front so we can record it in the WAL
	// before calling Bifrost. If a crash happens between create and lease
	// persist, walRollback finds the orphan by this name (docs/06).
	name, err := renderName(role.NameTemplate, roleName)
	if err != nil {
		return nil, err
	}

	walID, err := framework.PutWAL(ctx, req.Storage, walKindVirtualKey, &walVirtualKey{
		Role:    roleName,
		Name:    name,
		IssueAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to write WAL: %w", err)
	}

	vk, err := b.createVirtualKey(ctx, client, role, roleName, name, req)
	if err != nil {
		// The VK was not durably created; drop the WAL and fail the read.
		// Any partial VK is reconciled by rollback via the recorded name.
		if walErr := framework.DeleteWAL(ctx, req.Storage, walID); walErr != nil {
			b.Logger().Warn("failed to delete WAL after create failure", "wal_id", walID, "error", walErr)
		}
		return nil, err
	}

	// Success: the lease's InternalData is now the source of truth, so the
	// WAL can be removed.
	if err := framework.DeleteWAL(ctx, req.Storage, walID); err != nil {
		// Non-fatal: a stale WAL is reconciled later and delete is idempotent.
		b.Logger().Warn("failed to delete WAL after successful issue", "wal_id", walID, "error", err)
	}

	resp := b.Secret(secretTypeVK).Response(
		map[string]interface{}{
			"value": vk.Value,
			"vk_id": vk.ID,
		},
		map[string]interface{}{
			"vk_id": vk.ID,
			"role":  roleName,
		},
	)
	resp.Secret.TTL = role.TTL
	resp.Secret.MaxTTL = role.MaxTTL

	return resp, nil
}

// createVirtualKey builds the create request from the role and issues the key.
func (b *backend) createVirtualKey(ctx context.Context, client *bifrostClient, role *bifrostRole, roleName, name string, req *logical.Request) (*virtualKey, error) {
	body := buildCreateVKRequest(role, roleName, name, req)
	return client.CreateVirtualKey(ctx, body)
}

// buildCreateVKRequest maps a role to a Bifrost create-virtual-key body (docs/04).
func buildCreateVKRequest(role *bifrostRole, roleName, name string, req *logical.Request) map[string]interface{} {
	body := map[string]interface{}{
		"name":             name,
		"provider_configs": role.ProviderConfigs,
		"is_active":        role.IsActive,
		"description":      fmt.Sprintf("Issued by Vault for role %q (request %s)", roleName, req.ID),
	}
	if len(role.Budgets) > 0 {
		body["budgets"] = role.Budgets
	}
	if role.RateLimit != nil {
		body["rate_limit"] = role.RateLimit
	}
	if role.SetExpiresAt {
		ttl := role.TTL
		if ttl <= 0 {
			ttl = time.Hour
		}
		// Backstop slightly beyond the lease so Bifrost doesn't expire the
		// key before Vault revokes it.
		body["expires_at"] = time.Now().UTC().Add(ttl + 5*time.Minute).Format(time.RFC3339)
	}

	return body
}

// renderName expands {{role}} and {{random}} in a name template, producing a
// unique virtual-key name per issue so concurrent reads don't collide.
func renderName(tmpl, roleName string) (string, error) {
	if tmpl == "" {
		tmpl = defaultNameTemplate
	}
	suffix, err := base62.Random(12)
	if err != nil {
		return "", fmt.Errorf("failed to generate name suffix: %w", err)
	}
	out := strings.ReplaceAll(tmpl, "{{role}}", roleName)
	out = strings.ReplaceAll(out, "{{random}}", suffix)
	return out, nil
}
