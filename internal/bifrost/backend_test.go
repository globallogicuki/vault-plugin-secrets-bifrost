package bifrost

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
)

// newTestBackend builds the backend against an in-memory Vault and a mock
// Bifrost, writing a valid config so creds/roles operations can run.
func newTestBackend(t *testing.T) (*backend, logical.Storage, *mockBifrost) {
	t.Helper()

	mock := newMockBifrost()
	t.Cleanup(mock.Close)

	config := logical.TestBackendConfig()
	config.StorageView = &logical.InmemStorage{}

	raw, err := Factory(context.Background(), config)
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	b := raw.(*backend)

	writeConfig(t, b, config.StorageView, mock)
	return b, config.StorageView, mock
}

func writeConfig(t *testing.T, b *backend, s logical.Storage, mock *mockBifrost) {
	t.Helper()
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "config",
		Storage:   s,
		Data: map[string]interface{}{
			"address":          mock.addr(),
			"management_token": testManagementToken,
		},
	})
	if err != nil || (resp != nil && resp.IsError()) {
		t.Fatalf("config write: err=%v resp=%v", err, resp)
	}
}

func writeRole(t *testing.T, b *backend, s logical.Storage, name string, data map[string]interface{}) *logical.Response {
	t.Helper()
	if data == nil {
		data = map[string]interface{}{
			"provider_configs": []interface{}{
				map[string]interface{}{"provider": "openai", "allowed_models": []interface{}{"gpt-4o"}},
			},
			"ttl":     "1h",
			"max_ttl": "24h",
		}
	}
	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "roles/" + name,
		Storage:   s,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("role write %q: %v", name, err)
	}
	return resp
}

func TestConfig_ReadRedactsManagementToken(t *testing.T) {
	b, s, _ := newTestBackend(t)

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation,
		Path:      "config",
		Storage:   s,
	})
	if err != nil || resp == nil {
		t.Fatalf("config read: err=%v resp=%v", err, resp)
	}
	if _, present := resp.Data["management_token"]; present {
		t.Fatal("config read must not return management_token")
	}
	if resp.Data["address"] == "" {
		t.Fatal("config read should return address")
	}
}

func TestRole_ValidationRequiresProviderConfigs(t *testing.T) {
	b, s, _ := newTestBackend(t)

	resp := writeRole(t, b, s, "bad", map[string]interface{}{
		"provider_configs": []interface{}{},
		"ttl":              "1h",
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("expected validation error for empty provider_configs, got %v", resp)
	}
}

func TestRole_TTLExceedingMaxRejected(t *testing.T) {
	b, s, _ := newTestBackend(t)

	resp := writeRole(t, b, s, "bad", map[string]interface{}{
		"provider_configs": []interface{}{
			map[string]interface{}{"provider": "openai"},
		},
		"ttl":     "48h",
		"max_ttl": "1h",
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("expected ttl>max_ttl error, got %v", resp)
	}
}

func TestCreds_IssueFramesLeaseWithValueAndID(t *testing.T) {
	b, s, mock := newTestBackend(t)
	writeRole(t, b, s, "web-app", nil)

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation,
		Path:      "creds/web-app",
		Storage:   s,
	})
	if err != nil || resp == nil {
		t.Fatalf("creds read: err=%v resp=%v", err, resp)
	}
	if resp.Secret == nil {
		t.Fatal("expected a secret/lease on creds read")
	}
	if got, _ := resp.Data["value"].(string); got == "" {
		t.Fatal("expected a virtual key value in the response")
	}
	vkID, _ := resp.Data["vk_id"].(string)
	if vkID == "" {
		t.Fatal("expected a vk_id in the response")
	}
	if resp.Secret.InternalData["vk_id"] != vkID {
		t.Fatal("vk_id should be stored in lease internal data")
	}
	if resp.Secret.InternalData["role"] != "web-app" {
		t.Fatal("role should be stored in lease internal data")
	}
	if mock.createCount.Load() != 1 {
		t.Fatalf("expected 1 create call, got %d", mock.createCount.Load())
	}
}

func TestCreds_UnknownRoleErrors(t *testing.T) {
	b, s, _ := newTestBackend(t)

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation,
		Path:      "creds/nope",
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("expected error response for unknown role, got %v", resp)
	}
}

func TestRevoke_DeletesVirtualKey(t *testing.T) {
	b, s, mock := newTestBackend(t)
	writeRole(t, b, s, "web-app", nil)

	issue, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation,
		Path:      "creds/web-app",
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	_, err = b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.RevokeOperation,
		Path:      "creds/web-app",
		Storage:   s,
		Secret:    issue.Secret,
	})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if mock.deleteCount.Load() != 1 {
		t.Fatalf("expected 1 delete call, got %d", mock.deleteCount.Load())
	}
	if n := len(mock.keys); n != 0 {
		t.Fatalf("expected 0 keys after revoke, got %d", n)
	}
}

func TestRevoke_NotFoundIsSuccess(t *testing.T) {
	b, s, mock := newTestBackend(t)
	writeRole(t, b, s, "web-app", nil)
	issue, _ := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation, Path: "creds/web-app", Storage: s,
	})
	// Bifrost returns 404 on delete (already gone) -> revoke should succeed.
	mock.failDeleteStatus = 404

	_, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.RevokeOperation,
		Path:      "creds/web-app",
		Storage:   s,
		Secret:    issue.Secret,
	})
	if err != nil {
		t.Fatalf("revoke with 404 should succeed, got: %v", err)
	}
}

func TestRevoke_ServerErrorIsRetryable(t *testing.T) {
	b, s, mock := newTestBackend(t)
	writeRole(t, b, s, "web-app", nil)
	issue, _ := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation, Path: "creds/web-app", Storage: s,
	})
	mock.failDeleteStatus = 500

	_, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.RevokeOperation,
		Path:      "creds/web-app",
		Storage:   s,
		Secret:    issue.Secret,
	})
	if err == nil {
		t.Fatal("expected a retryable error when Bifrost returns 500 on delete")
	}
	if !isRetryable(err) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestWALRollback_DeletesOrphanedKey(t *testing.T) {
	b, s, mock := newTestBackend(t)
	writeRole(t, b, s, "web-app", nil)
	client, err := b.getClient(context.Background(), s)
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}

	// Simulate an orphan: a VK created in Bifrost whose lease was never
	// persisted, with a WAL recording its name.
	const orphanName = "vault-web-app-orphaned"
	vk, err := client.CreateVirtualKey(context.Background(), map[string]interface{}{
		"name":             orphanName,
		"provider_configs": []interface{}{map[string]interface{}{"provider": "openai"}},
	})
	if err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	if mock.countKeysNamed(orphanName) != 1 {
		t.Fatal("orphan should exist before rollback")
	}

	if err := b.walRollback(context.Background(), &logical.Request{Storage: s}, walKindVirtualKey, map[string]interface{}{
		"role": "web-app",
		"name": orphanName,
	}); err != nil {
		t.Fatalf("walRollback: %v", err)
	}
	if mock.countKeysNamed(orphanName) != 0 {
		t.Fatalf("orphan %s (id %s) should be deleted by rollback", orphanName, vk.ID)
	}
}

func TestRotateRoot_RequiresSuppliedToken(t *testing.T) {
	b, s, _ := newTestBackend(t)

	resp, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "config/rotate-root",
		Storage:   s,
	})
	if err != nil {
		t.Fatalf("rotate-root: %v", err)
	}
	if resp == nil || !resp.IsError() {
		t.Fatalf("expected error requiring a supplied token, got %v", resp)
	}
}

func TestRotateRoot_SwapsToken(t *testing.T) {
	b, s, _ := newTestBackend(t)

	_, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "config/rotate-root",
		Storage:   s,
		Data:      map[string]interface{}{"management_token": "new-token"},
	})
	if err != nil {
		t.Fatalf("rotate-root: %v", err)
	}
	cfg, err := getConfig(context.Background(), s)
	if err != nil {
		t.Fatalf("getConfig: %v", err)
	}
	if cfg.ManagementToken != "new-token" {
		t.Fatalf("token not swapped: %q", cfg.ManagementToken)
	}
}

// isRetryable reports whether err is (or wraps) a retryableError.
func isRetryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}
