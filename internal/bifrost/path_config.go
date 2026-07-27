package bifrost

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/helper/automatedrotationutil"
	"github.com/hashicorp/vault/sdk/logical"
	"github.com/hashicorp/vault/sdk/rotation"
)

const (
	configStoragePath = "config"
)

// errBackendNotConfigured is returned when an operation needs the Bifrost
// connection but no config entry exists yet.
var errBackendNotConfigured = errors.New("bifrost: backend not configured; write config first")

// bifrostConfig is the persisted connection + rotation configuration. One entry
// per mount. The management token is stored Vault-encrypted and never read back.
type bifrostConfig struct {
	Address               string `json:"address"`
	ManagementToken       string `json:"management_token"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	TLSCACert             string `json:"tls_ca_cert"`
	TLSSkipVerify         bool   `json:"tls_skip_verify"`
	APIVersion            string `json:"api_version"`

	// Embedded standard Vault automated-rotation parameters
	// (rotation_period / rotation_schedule / rotation_window /
	// disable_automated_rotation).
	automatedrotationutil.AutomatedRotationParams
}

func pathConfig(b *backend) []*framework.Path {
	fields := map[string]*framework.FieldSchema{
		"address": {
			Type:        framework.TypeString,
			Description: "Base URL of the Bifrost management API, e.g. https://bifrost.internal:8080.",
			Required:    true,
		},
		"management_token": {
			Type:        framework.TypeString,
			Description: "Bearer token for Bifrost /api/* management endpoints. Write-only; never read back.",
			DisplayAttrs: &framework.DisplayAttributes{
				Sensitive: true,
			},
		},
		"request_timeout": {
			Type:        framework.TypeDurationSecond,
			Default:     30,
			Description: "Per-request timeout for calls to Bifrost. Defaults to 30s.",
		},
		"tls_ca_cert": {
			Type:        framework.TypeString,
			Description: "PEM-encoded CA bundle used to verify the Bifrost server certificate.",
		},
		"tls_skip_verify": {
			Type:        framework.TypeBool,
			Default:     false,
			Description: "Disable TLS verification when calling Bifrost. Development only; unsafe.",
		},
		"api_version": {
			Type:        framework.TypeString,
			Description: "Optional Bifrost API version to record/pin.",
		},
	}
	// Add rotation_period / rotation_schedule / rotation_window /
	// disable_automated_rotation using Vault's standard helper.
	automatedrotationutil.AddAutomatedRotationFields(fields)

	return []*framework.Path{
		{
			Pattern: "config$",
			Fields:  fields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.CreateOperation: &framework.PathOperation{Callback: b.pathConfigWrite},
				logical.UpdateOperation: &framework.PathOperation{Callback: b.pathConfigWrite},
				logical.ReadOperation:   &framework.PathOperation{Callback: b.pathConfigRead},
			},
			ExistenceCheck:  b.configExists,
			HelpSynopsis:    "Configure the Bifrost connection and management token.",
			HelpDescription: "Stores the Bifrost address, management (bearer) token, TLS settings, and automated management-token rotation schedule. The management token is never returned by a read.",
		},
	}
}

func (b *backend) configExists(ctx context.Context, req *logical.Request, _ *framework.FieldData) (bool, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return false, err
	}
	return cfg != nil, nil
}

func (b *backend) pathConfigWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &bifrostConfig{}
	}

	if v, ok := d.GetOk("address"); ok {
		cfg.Address = v.(string)
	}
	if v, ok := d.GetOk("management_token"); ok {
		cfg.ManagementToken = v.(string)
	}
	if v, ok := d.GetOk("request_timeout"); ok {
		cfg.RequestTimeoutSeconds = v.(int)
	}
	if v, ok := d.GetOk("tls_ca_cert"); ok {
		cfg.TLSCACert = v.(string)
	}
	if v, ok := d.GetOk("tls_skip_verify"); ok {
		cfg.TLSSkipVerify = v.(bool)
	}
	if v, ok := d.GetOk("api_version"); ok {
		cfg.APIVersion = v.(string)
	}

	if err := cfg.ParseAutomatedRotationFields(d); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	if req.Operation == logical.CreateOperation {
		if cfg.Address == "" {
			return logical.ErrorResponse("address is required"), nil
		}
		if cfg.ManagementToken == "" {
			return logical.ErrorResponse("management_token is required"), nil
		}
	}

	// Validate the config produces a usable client before persisting.
	if _, err := newBifrostClient(cfg); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	if err := putConfig(ctx, req.Storage, cfg); err != nil {
		return nil, err
	}
	// Any change invalidates the cached client.
	b.clearClient()

	// Register or deregister the scheduled rotation job to match the config.
	resp := &logical.Response{}
	if err := b.syncRotationJob(ctx, req, cfg, resp); err != nil {
		return nil, err
	}
	if len(resp.Warnings) == 0 {
		return nil, nil
	}
	return resp, nil
}

func (b *backend) pathConfigRead(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	cfg, err := getConfig(ctx, req.Storage)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}

	// Deliberately omit management_token.
	data := map[string]interface{}{
		"address":         cfg.Address,
		"request_timeout": cfg.RequestTimeoutSeconds,
		"tls_ca_cert":     cfg.TLSCACert,
		"tls_skip_verify": cfg.TLSSkipVerify,
		"api_version":     cfg.APIVersion,
	}
	cfg.PopulateAutomatedRotationData(data)

	return &logical.Response{Data: data}, nil
}

// syncRotationJob registers or deregisters the management-token rotation job
// with Vault's rotation manager to match the config. It only touches the
// rotation manager when rotation is actually in use (nonzero schedule/period)
// or explicitly disabled, so a plain config write doesn't need rotation
// support. On Vault CE (no rotation manager) it degrades to a warning rather
// than failing the write.
func (b *backend) syncRotationJob(ctx context.Context, req *logical.Request, cfg *bifrostConfig, resp *logical.Response) error {
	switch {
	case cfg.ShouldRegisterRotationJob():
		job := &rotation.RotationJobConfigureRequest{
			MountPoint:       req.MountPoint,
			ReqPath:          configStoragePath,
			RotationSchedule: cfg.RotationSchedule,
			RotationWindow:   cfg.RotationWindow,
			RotationPeriod:   cfg.RotationPeriod,
		}
		if _, err := b.System().RegisterRotationJob(ctx, job); err != nil {
			if isRotationUnsupported(err) {
				resp.AddWarning("automated rotation is not supported by this Vault edition; the schedule was saved but will not run. Use config/rotate-root to rotate on demand.")
				return nil
			}
			return err
		}

	case cfg.DisableAutomatedRotation:
		// Only deregister when the operator explicitly disables rotation;
		// a plain config write with no rotation values leaves things alone.
		err := b.System().DeregisterRotationJob(ctx, &rotation.RotationJobDeregisterRequest{
			MountPoint: req.MountPoint,
			ReqPath:    configStoragePath,
		})
		if err != nil && !isRotationUnsupported(err) {
			return err
		}
	}
	return nil
}

// isRotationUnsupported reports whether an error indicates the running Vault
// has no rotation manager (community edition or a static test system view).
func isRotationUnsupported(err error) bool {
	if errors.Is(err, automatedrotationutil.ErrRotationManagerUnsupported) {
		return true
	}
	// StaticSystemView (and CE) surface this as a plain "not implemented" error.
	return strings.Contains(err.Error(), "not implemented")
}

// getConfig loads and decodes the config entry, or returns (nil, nil) if unset.
func getConfig(ctx context.Context, s logical.Storage) (*bifrostConfig, error) {
	entry, err := s.Get(ctx, configStoragePath)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	cfg := &bifrostConfig{}
	if err := entry.DecodeJSON(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func putConfig(ctx context.Context, s logical.Storage, cfg *bifrostConfig) error {
	entry, err := logical.StorageEntryJSON(configStoragePath, cfg)
	if err != nil {
		return err
	}
	return s.Put(ctx, entry)
}
