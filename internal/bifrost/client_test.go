package bifrost

import (
	"context"
	"strings"
	"testing"
)

func TestClient_CreateCapturesIDAndValue(t *testing.T) {
	mock := newMockBifrost()
	defer mock.Close()

	client, err := newBifrostClient(&bifrostConfig{Address: mock.addr(), ManagementToken: testManagementToken})
	if err != nil {
		t.Fatalf("newBifrostClient: %v", err)
	}

	vk, err := client.CreateVirtualKey(context.Background(), map[string]interface{}{"name": "k1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if vk.ID == "" || !strings.HasPrefix(vk.Value, "sk-bf-") {
		t.Fatalf("unexpected vk: %+v", vk)
	}
}

func TestClient_DeleteNotFoundMapsToSentinel(t *testing.T) {
	mock := newMockBifrost()
	defer mock.Close()
	client, _ := newBifrostClient(&bifrostConfig{Address: mock.addr(), ManagementToken: testManagementToken})

	err := client.DeleteVirtualKey(context.Background(), "does-not-exist")
	if !isNotFound(err) {
		t.Fatalf("expected errNotFound, got %v", err)
	}
}

func TestClient_ServerErrorIsRetryable(t *testing.T) {
	mock := newMockBifrost()
	defer mock.Close()
	mock.failCreateStatus = 503
	client, _ := newBifrostClient(&bifrostConfig{Address: mock.addr(), ManagementToken: testManagementToken})

	_, err := client.CreateVirtualKey(context.Background(), map[string]interface{}{"name": "k1"})
	if !isRetryable(err) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestClient_AuthFailureIsClearAndNotRetryable(t *testing.T) {
	mock := newMockBifrost()
	defer mock.Close()
	client, _ := newBifrostClient(&bifrostConfig{Address: mock.addr(), ManagementToken: "wrong-token"})

	_, err := client.CreateVirtualKey(context.Background(), map[string]interface{}{"name": "k1"})
	if err == nil {
		t.Fatal("expected auth error")
	}
	if isRetryable(err) {
		t.Fatal("auth failures should not be retryable")
	}
	if !strings.Contains(err.Error(), "config/rotate-root") {
		t.Fatalf("auth error should hint at config/rotate-root, got: %v", err)
	}
}

func TestRedactSecrets(t *testing.T) {
	token := "mgmt-super-secret"
	in := "failed with token " + token + " and key sk-bf-abc123DEF, done"
	out := redactSecrets(in, token)

	if strings.Contains(out, token) {
		t.Fatalf("management token not redacted: %q", out)
	}
	if strings.Contains(out, "sk-bf-abc123DEF") {
		t.Fatalf("virtual key value not redacted: %q", out)
	}
	if !strings.Contains(out, "[redacted-management-token]") || !strings.Contains(out, "[redacted-virtual-key]") {
		t.Fatalf("expected redaction markers, got: %q", out)
	}
}

func TestBuildCreateVKRequest_MapsRole(t *testing.T) {
	role := &bifrostRole{
		ProviderConfigs: []map[string]interface{}{{"provider": "openai"}},
		RateLimit:       map[string]interface{}{"request_max_limit": 10},
		IsActive:        true,
		SetExpiresAt:    true,
	}
	body := buildCreateVKRequest(role, "web-app", "vault-web-app-xyz", testRequest())

	if body["name"] != "vault-web-app-xyz" {
		t.Fatalf("name not set: %v", body["name"])
	}
	if _, ok := body["provider_configs"]; !ok {
		t.Fatal("provider_configs missing")
	}
	if _, ok := body["expires_at"]; !ok {
		t.Fatal("expires_at should be set when set_expires_at is true")
	}
	if _, ok := body["rate_limit"]; !ok {
		t.Fatal("rate_limit should be forwarded")
	}
}

func TestRenderName_IncludesDisplayNameAndRole(t *testing.T) {
	name, err := renderName(defaultNameTemplate, "web-app", "userpass-alice")
	if err != nil {
		t.Fatalf("renderName: %v", err)
	}
	if !strings.Contains(name, "web-app") {
		t.Errorf("name should contain the role: %q", name)
	}
	if !strings.Contains(name, "userpass-alice") {
		t.Errorf("name should contain the display name for traceability: %q", name)
	}
	// Two issues must not collide (random suffix differs).
	other, _ := renderName(defaultNameTemplate, "web-app", "userpass-alice")
	if name == other {
		t.Errorf("expected unique names per issue, got %q twice", name)
	}
}

func TestSanitiseNamePart(t *testing.T) {
	cases := []struct{ in, want string }{
		{"userpass-alice", "userpass-alice"},
		{"token", "token"},
		{"alice@example.com", "alice-example-com"},
		{"weird  name!!", "weird-name"},
		{"---", "unknown"},
		{"", "unknown"},
		{"OK_123", "OK_123"},
	}
	for _, c := range cases {
		if got := sanitiseNamePart(c.in); got != c.want {
			t.Errorf("sanitiseNamePart(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
