package bifrost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/vault/sdk/framework"
	"github.com/hashicorp/vault/sdk/logical"
)

const (
	rolesStoragePrefix = "role/"

	defaultNameTemplate = "vault-{{display_name}}-{{role}}-{{random}}"
)

// bifrostRole templates the virtual keys that creds/<name> issues. It maps onto
// the Bifrost create-virtual-key request (docs/04, docs/05).
type bifrostRole struct {
	ProviderConfigs []map[string]interface{} `json:"provider_configs"`
	Budgets         []map[string]interface{} `json:"budgets,omitempty"`
	RateLimit       map[string]interface{}   `json:"rate_limit,omitempty"`
	IsActive        bool                     `json:"is_active"`
	TTL             time.Duration            `json:"ttl"`
	MaxTTL          time.Duration            `json:"max_ttl"`
	NameTemplate    string                   `json:"name_template"`
	SetExpiresAt    bool                     `json:"set_expires_at"`
}

func pathRoles(b *backend) []*framework.Path {
	roleFields := map[string]*framework.FieldSchema{
		"name": {
			Type:        framework.TypeLowerCaseString,
			Description: "Name of the role.",
		},
		"provider_configs": {
			Type:        framework.TypeSlice,
			Description: "List of Bifrost provider_configs objects (provider, weight, allowed_models, budgets, rate_limit). At least one is required.",
		},
		"budgets": {
			Type:        framework.TypeSlice,
			Description: "Optional key-level budgets, each with max_limit and reset_duration.",
		},
		"rate_limit": {
			Type:        framework.TypeMap,
			Description: "Optional key-level rate_limit object.",
		},
		"is_active": {
			Type:        framework.TypeBool,
			Default:     true,
			Description: "Whether issued virtual keys are active.",
		},
		"ttl": {
			Type:        framework.TypeDurationSecond,
			Description: "Default lease TTL for issued keys. Clamped to the mount default.",
		},
		"max_ttl": {
			Type:        framework.TypeDurationSecond,
			Description: "Maximum lease TTL for issued keys. Clamped to the mount max.",
		},
		"name_template": {
			Type:        framework.TypeString,
			Default:     defaultNameTemplate,
			Description: "Template for the virtual key name. Supports {{role}}, {{display_name}} (the calling token/entity's display name, sanitised) and {{random}}.",
		},
		"set_expires_at": {
			Type:        framework.TypeBool,
			Default:     true,
			Description: "If true, set the virtual key's expires_at to about now+ttl as a Bifrost-side backstop.",
		},
	}

	return []*framework.Path{
		{
			Pattern: "roles/" + framework.GenericNameRegex("name"),
			Fields:  roleFields,
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.CreateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
				logical.UpdateOperation: &framework.PathOperation{Callback: b.pathRoleWrite},
				logical.ReadOperation:   &framework.PathOperation{Callback: b.pathRoleRead},
				logical.DeleteOperation: &framework.PathOperation{Callback: b.pathRoleDelete},
			},
			ExistenceCheck:  b.roleExists,
			HelpSynopsis:    "Manage a virtual-key role.",
			HelpDescription: "A role templates the scope (providers, budgets, rate limits) and TTLs of the virtual keys issued at creds/<name>.",
		},
		{
			Pattern: "roles/?$",
			Operations: map[logical.Operation]framework.OperationHandler{
				logical.ListOperation: &framework.PathOperation{Callback: b.pathRoleList},
			},
			HelpSynopsis: "List configured roles.",
		},
	}
}

func (b *backend) roleExists(ctx context.Context, req *logical.Request, d *framework.FieldData) (bool, error) {
	role, err := getRole(ctx, req.Storage, d.Get("name").(string))
	if err != nil {
		return false, err
	}
	return role != nil, nil
}

func (b *backend) pathRoleWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)
	if name == "" {
		return logical.ErrorResponse("role name is required"), nil
	}

	role, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if role == nil {
		role = &bifrostRole{
			IsActive:     true,
			NameTemplate: defaultNameTemplate,
			SetExpiresAt: true,
		}
	}

	if v, ok := d.GetOk("provider_configs"); ok {
		pc, err := toObjectList(v)
		if err != nil {
			return logical.ErrorResponse("provider_configs: %s", err), nil
		}
		role.ProviderConfigs = pc
	}
	if v, ok := d.GetOk("budgets"); ok {
		bud, err := toObjectList(v)
		if err != nil {
			return logical.ErrorResponse("budgets: %s", err), nil
		}
		role.Budgets = bud
	}
	if v, ok := d.GetOk("rate_limit"); ok {
		role.RateLimit = v.(map[string]interface{})
	}
	if v, ok := d.GetOk("is_active"); ok {
		role.IsActive = v.(bool)
	}
	if v, ok := d.GetOk("ttl"); ok {
		role.TTL = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("max_ttl"); ok {
		role.MaxTTL = time.Duration(v.(int)) * time.Second
	}
	if v, ok := d.GetOk("name_template"); ok {
		role.NameTemplate = v.(string)
	}
	if v, ok := d.GetOk("set_expires_at"); ok {
		role.SetExpiresAt = v.(bool)
	}

	if err := validateRole(role); err != nil {
		return logical.ErrorResponse(err.Error()), nil
	}

	entry, err := logical.StorageEntryJSON(rolesStoragePrefix+name, role)
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}
	return nil, nil
}

func (b *backend) pathRoleRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	role, err := getRole(ctx, req.Storage, d.Get("name").(string))
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, nil
	}

	return &logical.Response{
		Data: map[string]interface{}{
			"provider_configs": role.ProviderConfigs,
			"budgets":          role.Budgets,
			"rate_limit":       role.RateLimit,
			"is_active":        role.IsActive,
			"ttl":              int64(role.TTL.Seconds()),
			"max_ttl":          int64(role.MaxTTL.Seconds()),
			"name_template":    role.NameTemplate,
			"set_expires_at":   role.SetExpiresAt,
		},
	}, nil
}

func (b *backend) pathRoleDelete(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	if err := req.Storage.Delete(ctx, rolesStoragePrefix+d.Get("name").(string)); err != nil {
		return nil, err
	}
	return nil, nil
}

func (b *backend) pathRoleList(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	names, err := req.Storage.List(ctx, rolesStoragePrefix)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(names), nil
}

// validateRole enforces the invariants from docs/05.
func validateRole(role *bifrostRole) error {
	if len(role.ProviderConfigs) == 0 {
		return errors.New("at least one provider_configs entry is required (Bifrost is deny-by-default with none)")
	}
	if role.MaxTTL > 0 && role.TTL > role.MaxTTL {
		return fmt.Errorf("ttl (%s) must not exceed max_ttl (%s)", role.TTL, role.MaxTTL)
	}
	for i, budget := range role.Budgets {
		if err := validateBudget(budget); err != nil {
			return fmt.Errorf("budgets[%d]: %w", i, err)
		}
	}
	return nil
}

func validateBudget(budget map[string]interface{}) error {
	if _, ok := budget["max_limit"]; !ok {
		return errors.New("max_limit is required")
	}
	if _, ok := budget["reset_duration"]; !ok {
		return errors.New("reset_duration is required")
	}
	return nil
}

// toObjectList coerces a decoded JSON slice into a list of objects.
func toObjectList(v interface{}) ([]map[string]interface{}, error) {
	raw, ok := v.([]interface{})
	if !ok {
		return nil, errors.New("expected a list of objects")
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for i, item := range raw {
		obj, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("element %d is not an object", i)
		}
		out = append(out, obj)
	}
	return out, nil
}

func getRole(ctx context.Context, s logical.Storage, name string) (*bifrostRole, error) {
	if name == "" {
		return nil, errors.New("role name is required")
	}
	entry, err := s.Get(ctx, rolesStoragePrefix+name)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	role := &bifrostRole{}
	if err := entry.DecodeJSON(role); err != nil {
		return nil, err
	}
	return role, nil
}
