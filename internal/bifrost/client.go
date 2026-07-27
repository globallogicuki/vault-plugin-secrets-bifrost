package bifrost

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Bifrost management API paths (see docs/04-bifrost-integration.md).
const (
	pathVirtualKeys = "/api/governance/virtual-keys"

	defaultRequestTimeout = 30 * time.Second
	maxErrorBodyBytes     = 4 << 10 // cap error bodies we read/echo
)

// errNotFound is returned when Bifrost reports a 404 for a virtual key. Revoke
// treats this as success so leases can always be cleaned up.
var errNotFound = errors.New("bifrost: virtual key not found")

// retryableError marks a failure Vault should retry (5xx / network / timeout).
// Vault re-invokes revoke on its own schedule when a retryable error surfaces.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func isNotFound(err error) bool { return errors.Is(err, errNotFound) }

// bifrostClient is a thin, dependency-light HTTP client over Bifrost's
// management API. It injects the bearer token and redacts secrets from errors.
type bifrostClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newBifrostClient(cfg *bifrostConfig) (*bifrostClient, error) {
	if cfg.Address == "" {
		return nil, errors.New("bifrost: config address is empty")
	}

	timeout := time.Duration(cfg.RequestTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}

	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.TLSSkipVerify, //nolint:gosec // dev-only, documented unsafe
	}
	if cfg.TLSCACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.TLSCACert)) {
			return nil, errors.New("bifrost: tls_ca_cert is not a valid PEM bundle")
		}
		tlsCfg.RootCAs = pool
	}

	return &bifrostClient{
		baseURL: strings.TrimRight(cfg.Address, "/"),
		token:   cfg.ManagementToken,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// virtualKey is the subset of a Bifrost virtual key the engine cares about.
type virtualKey struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// createVKResponse matches Bifrost's create virtual-key response envelope.
type createVKResponse struct {
	Message    string     `json:"message"`
	VirtualKey virtualKey `json:"virtual_key"`
}

// CreateVirtualKey issues a virtual key and returns its id and secret value.
// The value is only present in the create response and must be captured here.
func (c *bifrostClient) CreateVirtualKey(ctx context.Context, body map[string]interface{}) (*virtualKey, error) {
	var out createVKResponse
	if err := c.do(ctx, http.MethodPost, pathVirtualKeys, body, &out); err != nil {
		return nil, err
	}
	if out.VirtualKey.ID == "" || out.VirtualKey.Value == "" {
		return nil, errors.New("bifrost: create virtual key response missing id or value")
	}
	return &out.VirtualKey, nil
}

// GetVirtualKey fetches a virtual key by id (used for verification/diagnostics).
func (c *bifrostClient) GetVirtualKey(ctx context.Context, id string) (*virtualKey, error) {
	var out struct {
		VirtualKey virtualKey `json:"virtual_key"`
	}
	if err := c.do(ctx, http.MethodGet, pathVirtualKeys+"/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out.VirtualKey, nil
}

// ListVirtualKeys returns all virtual keys. Used by WAL rollback to find an
// orphan by the name the engine assigned it.
func (c *bifrostClient) ListVirtualKeys(ctx context.Context) ([]virtualKey, error) {
	var out struct {
		VirtualKeys []virtualKey `json:"virtual_keys"`
	}
	if err := c.do(ctx, http.MethodGet, pathVirtualKeys, nil, &out); err != nil {
		return nil, err
	}
	return out.VirtualKeys, nil
}

// UpdateVirtualKey patches a virtual key (e.g. to push expires_at forward on
// renew so the Bifrost-side backstop stays ahead of the Vault lease).
func (c *bifrostClient) UpdateVirtualKey(ctx context.Context, id string, body map[string]interface{}) error {
	return c.do(ctx, http.MethodPut, pathVirtualKeys+"/"+id, body, nil)
}

// DeleteVirtualKey removes a virtual key. A 404 is reported as errNotFound so
// callers can treat an already-deleted key as success (idempotent revoke).
func (c *bifrostClient) DeleteVirtualKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, pathVirtualKeys+"/"+id, nil, nil)
}

// do performs a request, maps status codes to typed errors, and decodes the
// response into out (when non-nil). Secrets are never placed in error strings.
func (c *bifrostClient) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("bifrost: encode request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("bifrost: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Network / timeout / cancellation: retryable so Vault retries revoke.
		return &retryableError{fmt.Errorf("bifrost: %s %s: %w", method, path, redactErr(err, c.token))}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("bifrost: %s %s: authentication failed (%d) - check the management token via config/rotate-root", method, path, resp.StatusCode)
	case resp.StatusCode == http.StatusConflict:
		return &conflictError{status: resp.StatusCode, detail: c.readError(resp.Body)}
	case resp.StatusCode >= 500:
		return &retryableError{fmt.Errorf("bifrost: %s %s: server error %d: %s", method, path, resp.StatusCode, c.readError(resp.Body))}
	case resp.StatusCode >= 400:
		return fmt.Errorf("bifrost: %s %s: request failed %d: %s", method, path, resp.StatusCode, c.readError(resp.Body))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("bifrost: decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

// conflictError signals a 409 (name/id collision) so the caller can regenerate
// the name and retry a bounded number of times.
type conflictError struct {
	status int
	detail string
}

func (e *conflictError) Error() string {
	return fmt.Sprintf("bifrost: conflict (%d): %s", e.status, e.detail)
}

// readError reads a bounded, token-redacted snippet of an error body.
func (c *bifrostClient) readError(r io.Reader) string {
	buf, _ := io.ReadAll(io.LimitReader(r, maxErrorBodyBytes))
	return redactSecrets(strings.TrimSpace(string(buf)), c.token)
}

// redactErr redacts the token from an error's message.
func redactErr(err error, token string) error {
	if err == nil {
		return nil
	}
	return errors.New(redactSecrets(err.Error(), token))
}

// redactSecrets removes the management token and any sk-bf- key values from a
// string before it can reach logs or error responses (see docs/07-security.md).
func redactSecrets(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "[redacted-management-token]")
	}
	return redactVKValues(s)
}

// redactVKValues masks any "sk-bf-..." substrings.
func redactVKValues(s string) string {
	const prefix = "sk-bf-"
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			return s
		}
		j := i + len(prefix)
		for j < len(s) && !isDelim(s[j]) {
			j++
		}
		s = s[:i] + "[redacted-virtual-key]" + s[j:]
	}
}

func isDelim(b byte) bool {
	switch b {
	case ' ', '"', '\'', ',', '}', ']', ')', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}
