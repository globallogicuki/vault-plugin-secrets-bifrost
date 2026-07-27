package bifrost

import (
	"context"
	"time"

	"github.com/hashicorp/vault/sdk/logical"
)

const (
	// walKindVirtualKey tags WAL entries recording an in-flight VK issue.
	walKindVirtualKey = "bifrost_vk"

	// walRollbackMinAge is how old a WAL must be before rollback acts on it,
	// so in-flight issues aren't clobbered mid-request.
	walRollbackMinAge = 10 * time.Minute
)

// walVirtualKey is the WAL payload recorded before creating a virtual key. The
// Name is the deterministic name assigned to the key, letting rollback find and
// delete an orphan created by a request that never persisted its lease.
type walVirtualKey struct {
	Role    string `json:"role"`
	Name    string `json:"name"`
	IssueAt string `json:"issue_at"`
}

// walRollback reconciles a stale WAL: if a virtual key with the recorded name
// still exists in Bifrost, it was orphaned by a crash between create and
// lease-persist, so delete it (docs/06 orphan prevention).
func (b *backend) walRollback(ctx context.Context, req *logical.Request, kind string, data interface{}) error {
	if kind != walKindVirtualKey {
		return nil
	}

	var entry walVirtualKey
	if err := mapToStruct(data, &entry); err != nil {
		return err
	}
	if entry.Name == "" {
		// Nothing actionable was recorded; drop the WAL.
		return nil
	}

	client, err := b.getClient(ctx, req.Storage)
	if err != nil {
		return err
	}

	keys, err := client.ListVirtualKeys(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}

	for _, vk := range keys {
		if vk.Name == entry.Name {
			if err := client.DeleteVirtualKey(ctx, vk.ID); err != nil && !isNotFound(err) {
				return err
			}
			b.Logger().Info("rolled back orphaned virtual key", "name", entry.Name, "vk_id", vk.ID)
			return nil
		}
	}
	// No matching key: either the issue completed or the key is already gone.
	return nil
}

// mapToStruct re-decodes the WAL's generic data into a typed struct.
func mapToStruct(data interface{}, out *walVirtualKey) error {
	m, ok := data.(map[string]interface{})
	if !ok {
		return nil
	}
	if v, ok := m["role"].(string); ok {
		out.Role = v
	}
	if v, ok := m["name"].(string); ok {
		out.Name = v
	}
	if v, ok := m["issue_at"].(string); ok {
		out.IssueAt = v
	}
	return nil
}
