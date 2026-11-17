package provider

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/managedstate"
	"github.com/yetone/magpie/internal/plugin"
)

type ManagedBinding struct {
	ID        string `json:"id"`
	SourceID  string `json:"source_id"`
	AccountID string `json:"account_id"`
	Name      string `json:"name,omitempty"`
}

// BindManaged exposes exactly the selected account under its Xfriend connection
// id. It never pools a second account merely because it shares a vendor.
func BindManaged(binding ManagedBinding) error {
	if !managedstate.Enabled() {
		return errors.New("managed binding outside model core")
	}
	if binding.ID == "" || binding.ID != Slug(binding.ID) || binding.SourceID == "" || binding.AccountID == "" {
		return errors.New("invalid managed account binding")
	}
	found := false
	for _, source := range append(plugin.Cached(), ManagedNativeSources()...) {
		if source.ID == binding.SourceID {
			for _, account := range source.Accounts {
				if account.Key == binding.AccountID {
					found = true
				}
			}
		}
	}
	if !found {
		return errors.New("managed plugin account not found")
	}
	raw, err := managedstate.Read("bindings")
	if err != nil {
		return err
	}
	var bindings map[string]ManagedBinding
	if err = json.Unmarshal(raw, &bindings); err != nil {
		return err
	}
	if bindings == nil {
		bindings = map[string]ManagedBinding{}
	}
	bindings[binding.ID] = binding
	raw, err = json.Marshal(bindings)
	if err != nil {
		return err
	}
	if err = managedstate.Write("bindings", raw); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

func DeleteManagedBinding(id string) error {
	raw, err := managedstate.Read("bindings")
	if err != nil {
		return err
	}
	var bindings map[string]ManagedBinding
	if err = json.Unmarshal(raw, &bindings); err != nil {
		return err
	}
	delete(bindings, id)
	raw, err = json.Marshal(bindings)
	if err != nil {
		return err
	}
	if err = managedstate.Write("bindings", raw); err != nil {
		return err
	}
	catalog.Touched()
	return nil
}

func managedPluginAccounts() []Provider {
	raw, err := managedstate.Read("bindings")
	if err != nil {
		return nil
	}
	bindings := map[string]ManagedBinding{}
	if json.Unmarshal(raw, &bindings) != nil {
		return nil
	}
	out := []Provider{}
	keys := make([]string, 0, len(bindings))
	for id := range bindings {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	for _, id := range keys {
		binding := bindings[id]
		if native, ok := managedNativeProvider(binding); ok {
			out = append(out, native)
			continue
		}
		for _, pp := range heldPlugins() {
			if pp.ID != binding.SourceID {
				continue
			}
			for _, login := range publicLogins(pp) {
				if login.acct.Key != binding.AccountID {
					continue
				}
				// This only reads the host's already materialized, owner-bound account.
				p := pluginProviderServing(pp, login, nil)
				p.ID, p.Preset = binding.ID, binding.SourceID
				if binding.Name != "" {
					p.Name = binding.Name
				}
				out = append(out, p)
			}
		}
	}
	return out
}

// ManagedPluginCatalog returns installed descriptors without discovering external
// clients. The plugin's own signed-in catalog is the source of truth.
func ManagedPluginCatalog() []plugin.Provider { return plugin.Cached() }

// ManagedPluginUsage queries only the account explicitly bound to this owner.
func ManagedPluginUsage(ctx context.Context, id string) (any, error) {
	raw, err := managedstate.Read("bindings")
	if err != nil {
		return nil, err
	}
	bindings := map[string]ManagedBinding{}
	if err = json.Unmarshal(raw, &bindings); err != nil {
		return nil, err
	}
	binding, ok := bindings[id]
	if !ok {
		return nil, errors.New("managed account binding not found")
	}
	if binding.SourceID == "claude" || binding.SourceID == "antigravity" {
		return managedNativeQuota(ctx, binding)
	}
	return plugin.AccountUsage(ctx, binding.SourceID, binding.AccountID)
}
