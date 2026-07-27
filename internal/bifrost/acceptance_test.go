package bifrost

import (
	"context"
	"os"
	"testing"

	"github.com/hashicorp/vault/sdk/logical"
)

// testRequest returns a minimal logical.Request for building request bodies.
func testRequest() *logical.Request {
	return &logical.Request{ID: "test-request-id"}
}

// TestAcc_IssueAndRevoke is an acceptance test against a real (or dockerised)
// Bifrost. It is skipped unless VAULT_ACC is set (docs/08).
//
// Required environment:
//   - VAULT_ACC=1
//   - BIFROST_ADDR: base URL of the Bifrost management API
//   - BIFROST_MANAGEMENT_TOKEN: a valid management bearer token
func TestAcc_IssueAndRevoke(t *testing.T) {
	if os.Getenv("VAULT_ACC") == "" {
		t.Skip("VAULT_ACC not set; skipping acceptance test")
	}
	addr := os.Getenv("BIFROST_ADDR")
	token := os.Getenv("BIFROST_MANAGEMENT_TOKEN")
	if addr == "" || token == "" {
		t.Fatal("BIFROST_ADDR and BIFROST_MANAGEMENT_TOKEN are required for acceptance tests")
	}

	config := logical.TestBackendConfig()
	config.StorageView = &logical.InmemStorage{}
	raw, err := Factory(context.Background(), config)
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	b := raw.(*backend)
	s := config.StorageView

	if _, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "config",
		Storage:   s,
		Data:      map[string]interface{}{"address": addr, "management_token": token},
	}); err != nil {
		t.Fatalf("config: %v", err)
	}

	if _, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.CreateOperation,
		Path:      "roles/acc",
		Storage:   s,
		Data: map[string]interface{}{
			"provider_configs": []interface{}{map[string]interface{}{"provider": "openai"}},
			"ttl":              "5m",
		},
	}); err != nil {
		t.Fatalf("role: %v", err)
	}

	issue, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.ReadOperation, Path: "creds/acc", Storage: s,
	})
	if err != nil || issue == nil || issue.Secret == nil {
		t.Fatalf("issue: err=%v resp=%v", err, issue)
	}
	if v, _ := issue.Data["value"].(string); v == "" {
		t.Fatal("expected a virtual key value")
	}

	// TODO: call Bifrost with the issued key to confirm it works, then assert
	// it stops working after revoke. Requires an inference endpoint + provider.

	if _, err := b.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.RevokeOperation, Path: "creds/acc", Storage: s, Secret: issue.Secret,
	}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
}
