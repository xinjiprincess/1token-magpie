// Package modeltransfer carries only explicitly selected model configuration
// between Magpie and Xfriend. Collect never discovers or refreshes other apps'
// sign-ins, installs plugins, or changes the source configuration.
package modeltransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const Purpose = "xfriend-model-transfer"
const Version = 1
const MaxBytes = 32 << 20

var ErrInvalid = errors.New("MODEL_TRANSFER_INVALID")
var ErrChanged = errors.New("MODEL_TRANSFER_CHANGED")
var ErrSelection = errors.New("MODEL_TRANSFER_SELECTION_INVALID")

// Provider is an executable-code-free provider configuration. Subscription
// credentials are never copied by this format; the destination must authorize.
type Provider struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Preset    string         `json:"preset,omitempty"`
	Kind      string         `json:"kind"`
	AuthState string         `json:"auth_state"`
	Config    map[string]any `json:"config"`
}
type Account struct {
	ID                string `json:"id"`
	ProviderID        string `json:"provider_id"`
	Name              string `json:"name"`
	Plan              string `json:"plan,omitempty"`
	AuthState         string `json:"auth_state"`
	TransferSupported bool   `json:"transfer_supported"`
}
type Plugin struct {
	Spec    string `json:"spec"`
	Off     bool   `json:"off,omitempty"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
}
type Manifest struct {
	Purpose    string           `json:"purpose"`
	Version    int              `json:"version"`
	SourceID   string           `json:"source_id"`
	CreatedAt  int64            `json:"created_at"`
	AppVersion string           `json:"app_version"`
	Providers  []Provider       `json:"providers"`
	Accounts   []Account        `json:"accounts"`
	Groups     []provider.Group `json:"groups"`
	Plugins    []Plugin         `json:"plugins"`
}
type ProviderPreview struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Kind              string `json:"kind"`
	Preset            string `json:"preset,omitempty"`
	AuthState         string `json:"auth_state"`
	CredentialPresent bool   `json:"credential_present"`
	ModelCount        int    `json:"model_count"`
}
type GroupPreview struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"member_count"`
}
type Preview struct {
	Fingerprint string            `json:"fingerprint"`
	SourceID    string            `json:"source_id"`
	Providers   []ProviderPreview `json:"providers"`
	Accounts    []Account         `json:"accounts"`
	Groups      []GroupPreview    `json:"groups"`
	Plugins     []Plugin          `json:"plugins"`
}
type Selection struct {
	Fingerprint string   `json:"fingerprint"`
	ProviderIDs []string `json:"provider_ids"`
	GroupIDs    []string `json:"group_ids"`
	IncludeKeys bool     `json:"include_keys"`
}

// Only fields that describe model endpoints, capabilities and routing travel.
// Client ownership, local paths, sync state and host execution settings do not.
var configFields = strings.Fields(`id name preset key keys keyName keyProtocol keyWeight keyPlan accessKeyID secretAccessKey chat responses anthropic gemini decide baseAPI fallback routing sink affinity maxConcurrency accountConcurrency accountWeights queueLimit queueWait maxRPM quotaRest quotaResetAt restCap limitRestOff priceRate countFree rateWindows headers extraBody replyHeaders codexAsSent promptAsWritten additionalTools patience nativeCompaction passUserAgent pinUpstream authHeader proxy accountProxies accountModels accountCaps accountWindowCaps capStop capCareful accountNames balanceURL balancePath balanceMethod balanceToken zhipuTeam vertex modelsURL models noModels bestInFamily pickedFrom delisted refreshEvery autoInclude unlisted hiddenDrawers off contexts family catalog website keysUrl hidden quiet tucked`)
var secretFields = strings.Fields(`key keys accessKeyID secretAccessKey balanceToken headers proxy accountProxies extraBody`)

var npmName = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)

func portablePlugin(spec string) bool {
	if plugin.IsPath(spec) || strings.HasPrefix(spec, "git+file:") {
		return false
	}
	if plugin.IsGit(spec) {
		raw := strings.TrimPrefix(spec, "git+")
		if strings.Contains(raw, "://") {
			u, err := url.Parse(raw)
			return err == nil && u.Host != "" && u.User == nil && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "ssh" || u.Scheme == "git")
		}
		return true
	}
	return npmName.MatchString(plugin.Name(strings.TrimPrefix(spec, "npm:")))
}
func stripEndpointPasswords(config map[string]any) {
	for _, field := range []string{"chat", "responses", "anthropic", "gemini", "decide", "modelsURL", "balanceURL", "website", "keysUrl"} {
		if value, ok := config[field].(string); ok {
			if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
				parsed.User = nil
				config[field] = parsed.String()
			}
		}
	}
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func config(p provider.Provider) map[string]any {
	data, _ := json.Marshal(p)
	var all map[string]any
	_ = json.Unmarshal(data, &all)
	out := map[string]any{}
	for _, key := range configFields {
		if value, ok := all[key]; ok {
			out[key] = value
		}
	}
	// A source-machine credentials path is never portable and must not be
	// opened by the receiving core as an implicit authority.
	if v, ok := out["vertex"].(map[string]any); ok {
		delete(v, "credentials")
		delete(v, "credentialsJSON")
	}
	return out
}
func hasKey(c map[string]any) bool {
	if value, ok := c["key"].(string); ok && value != "" {
		return true
	}
	if values, ok := c["keys"].([]any); ok {
		for _, v := range values {
			if m, ok := v.(map[string]any); ok {
				if k, ok := m["key"].(string); ok && k != "" {
					return true
				}
			}
		}
	}
	return false
}

// FromProvider wraps a pure parsed provider in the same model-only review
// format. It does not discover accounts, save configuration or fetch anything.
func FromProvider(p provider.Provider, sourceID, version string) (Manifest, error) {
	m := Manifest{Purpose: Purpose, Version: Version, SourceID: sourceID, CreatedAt: time.Now().Unix(), AppVersion: version,
		Providers: []Provider{{ID: p.ID, Name: p.Name, Preset: p.Preset, Kind: "api", AuthState: "ready", Config: config(p)}},
		Accounts:  []Account{}, Groups: []provider.Group{}, Plugins: []Plugin{}}
	return m, Validate(m)
}
func readJSON(path string, out any) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("model source is unreadable: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil || len(data) > MaxBytes {
		return ErrInvalid
	}
	if json.Unmarshal(data, out) != nil {
		return ErrInvalid
	}
	return nil
}

// Collect reads only Magpie's own saved configuration. It does not use
// provider.All/Logins, whose discovery can touch source clients or refresh auth.
func Collect(appVersion string) (Manifest, error) {
	// Read providers and groups from one file generation, not two independent
	// reads while a running source might save a changed routing group.
	var saved struct {
		Providers []provider.Provider `json:"providers"`
		Groups    []provider.Group    `json:"groups"`
	}
	if err := readJSON(provider.Path(), &saved); err != nil {
		return Manifest{}, err
	}
	ps, gs := saved.Providers, saved.Groups
	source, err := filepath.Abs(settings.Dir())
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Purpose: Purpose, Version: Version, SourceID: hash([]byte(source)), CreatedAt: time.Now().Unix(), AppVersion: appVersion, Providers: []Provider{}, Accounts: []Account{}, Groups: gs, Plugins: []Plugin{}}
	if m.Groups == nil {
		m.Groups = []provider.Group{}
	}
	for _, p := range ps {
		kind, state := "api", "ready"
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" && p.Gemini == "" && p.Decide == "" {
			kind, state = "subscription", "needs_reauth"
		}
		c := config(p)
		if p.IsVertex() {
			kind, state = "api", "needs_reauth"
		}
		if kind == "subscription" {
			for _, key := range secretFields {
				delete(c, key)
			}
		}
		m.Providers = append(m.Providers, Provider{ID: p.ID, Name: p.Name, Preset: p.Preset, Kind: kind, AuthState: state, Config: c})
	}
	var list plugin.List
	if err := readJSON(plugin.ListPath(), &list); err != nil {
		return Manifest{}, err
	}
	for _, entry := range list.Plugins {
		if portablePlugin(entry.Spec) {
			m.Plugins = append(m.Plugins, Plugin{Spec: entry.Spec, Off: entry.Off, Version: plugin.Version(entry.Spec), Commit: plugin.GitCommit(entry.Spec)})
		}
	}
	var logins []struct {
		ID    string `json:"id"`
		Agent string `json:"agent"`
		User  string `json:"user"`
		Plan  string `json:"plan"`
	}
	if err := readJSON(filepath.Join(settings.Dir(), "logins.json"), &logins); err != nil {
		return Manifest{}, err
	}
	for _, a := range logins {
		if a.Agent == "" {
			continue
		}
		id := a.ID
		if id == "" {
			id = hash([]byte(a.Agent + "\x00" + a.User))[:16]
		}
		m.Accounts = append(m.Accounts, Account{ID: id, ProviderID: a.Agent, Name: mask(a.User), Plan: a.Plan, AuthState: "needs_reauth"})
	}
	// The plugin auth file is decoded only into safe identity fields: raw tokens,
	// refresh credentials and provider-defined objects never enter the manifest.
	var auth map[string]struct {
		AccountID string `json:"accountId"`
		Email     string `json:"email"`
		Metadata  struct {
			Email string `json:"email"`
		} `json:"metadata"`
	}
	if err := readJSON(plugin.AuthPath(), &auth); err != nil {
		return Manifest{}, err
	}
	keys := make([]string, 0, len(auth))
	for key := range auth {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		a := auth[key]
		name := a.AccountID
		if name == "" {
			name = a.Metadata.Email
		}
		if name == "" {
			name = a.Email
		}
		if name == "" {
			name = "Saved account"
		}
		m.Accounts = append(m.Accounts, Account{ID: hash([]byte(key))[:16], ProviderID: provider.PluginID(plugin.ProviderOf(key)), Name: mask(name), AuthState: "needs_reauth"})
	}
	seen := map[string]bool{}
	for _, p := range m.Providers {
		seen[p.ID] = true
	}
	for _, a := range m.Accounts {
		if !seen[a.ProviderID] {
			m.Providers = append(m.Providers, Provider{ID: a.ProviderID, Name: a.ProviderID, Kind: "subscription", AuthState: "needs_reauth", Config: map[string]any{"id": a.ProviderID, "name": a.ProviderID}})
			seen[a.ProviderID] = true
		}
	}
	return m, Validate(m)
}
func mask(value string) string {
	r := []rune(value)
	if len(r) <= 2 {
		return "***"
	}
	return string(r[:1]) + "***" + string(r[len(r)-1:])
}

// Validate refuses unknown top-level code-bearing fields on Decode and checks
// identity and provider payload shape before a destination considers import.
func Validate(m Manifest) error {
	if m.Purpose != Purpose || m.Version != Version || m.SourceID == "" {
		return ErrInvalid
	}
	if len(m.Providers) > 1000 || len(m.Groups) > 1000 || len(m.Accounts) > 5000 || len(m.Plugins) > 1000 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range m.Providers {
		if p.ID == "" || p.ID != provider.Slug(p.ID) || seen[p.ID] || (p.Kind != "api" && p.Kind != "subscription") || (p.AuthState != "ready" && p.AuthState != "needs_reauth") {
			return ErrInvalid
		}
		seen[p.ID] = true
		if p.Config == nil {
			return ErrInvalid
		}
		for key := range p.Config {
			if !slices.Contains(configFields, key) {
				return ErrInvalid
			}
		}
		if id, ok := p.Config["id"]; ok && id != p.ID {
			return ErrInvalid
		}
		if p.Kind == "subscription" {
			for _, key := range secretFields {
				if _, ok := p.Config[key]; ok {
					return ErrInvalid
				}
			}
		}
		raw, err := json.Marshal(p.Config)
		if err != nil {
			return ErrInvalid
		}
		var cfg provider.Provider
		if json.Unmarshal(raw, &cfg) != nil {
			return ErrInvalid
		}
		if cfg.Vertex != nil && (cfg.Vertex.Credentials != "" || cfg.Vertex.CredentialsJSON != "" || (cfg.IsVertex() && p.AuthState != "needs_reauth")) {
			return ErrInvalid
		}
	}
	seenGroups := map[string]bool{}
	for _, g := range m.Groups {
		if g.ID == "" || g.ID != provider.GroupSlug(g.ID) || seenGroups[g.ID] {
			return ErrInvalid
		}
		seenGroups[g.ID] = true
	}
	for _, p := range m.Plugins {
		if !portablePlugin(p.Spec) {
			return ErrInvalid
		}
	}
	for _, a := range m.Accounts {
		if a.ID == "" || a.ProviderID == "" || a.AuthState != "needs_reauth" || a.TransferSupported {
			return ErrInvalid
		}
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > MaxBytes {
		return ErrInvalid
	}
	return nil
}
func Decode(data []byte) (Manifest, error) {
	if len(data) > MaxBytes {
		return Manifest{}, ErrInvalid
	}
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return Manifest{}, ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return Manifest{}, ErrInvalid
	}
	return m, Validate(m)
}
func Fingerprint(m Manifest) string { m.CreatedAt = 0; raw, _ := json.Marshal(m); return hash(raw) }
func Inspect(m Manifest) (Preview, error) {
	if err := Validate(m); err != nil {
		return Preview{}, err
	}
	p := Preview{Fingerprint: Fingerprint(m), SourceID: m.SourceID, Providers: []ProviderPreview{}, Accounts: m.Accounts, Groups: []GroupPreview{}, Plugins: m.Plugins}
	for _, item := range m.Providers {
		models, _ := item.Config["models"].([]any)
		p.Providers = append(p.Providers, ProviderPreview{ID: item.ID, Name: item.Name, Preset: item.Preset, Kind: item.Kind, AuthState: item.AuthState, CredentialPresent: hasKey(item.Config), ModelCount: len(models)})
	}
	for _, g := range m.Groups {
		p.Groups = append(p.Groups, GroupPreview{ID: g.ID, Name: g.Name, MemberCount: len(g.Members)})
	}
	return p, nil
}
func Select(m Manifest, s Selection) (Manifest, error) {
	if err := Validate(m); err != nil {
		return Manifest{}, err
	}
	if s.Fingerprint == "" || s.Fingerprint != Fingerprint(m) {
		return Manifest{}, ErrChanged
	}
	raw, _ := json.Marshal(m)
	out, err := Decode(raw)
	if err != nil {
		return Manifest{}, err
	}
	wanted := map[string]bool{}
	for _, id := range s.ProviderIDs {
		if wanted[id] {
			return Manifest{}, ErrSelection
		}
		wanted[id] = true
	}
	groups := map[string]bool{}
	for _, id := range s.GroupIDs {
		if groups[id] {
			return Manifest{}, ErrSelection
		}
		groups[id] = true
	}
	clonedProviders := out.Providers
	out.Providers = []Provider{}
	out.Groups = []provider.Group{}
	out.Accounts = []Account{}
	for _, p := range clonedProviders {
		if !wanted[p.ID] {
			continue
		}
		delete(wanted, p.ID)
		if !s.IncludeKeys {
			for _, key := range secretFields {
				delete(p.Config, key)
			}
			stripEndpointPasswords(p.Config)
			p.AuthState = "needs_reauth"
		}
		out.Providers = append(out.Providers, p)
	}
	if len(wanted) > 0 {
		return Manifest{}, ErrSelection
	}
	for _, g := range m.Groups {
		if groups[g.ID] {
			out.Groups = append(out.Groups, g)
			delete(groups, g.ID)
		}
	}
	if len(groups) > 0 {
		return Manifest{}, ErrSelection
	}
	selected := map[string]bool{}
	for _, p := range out.Providers {
		selected[p.ID] = true
	}
	for _, a := range m.Accounts {
		if selected[a.ProviderID] {
			out.Accounts = append(out.Accounts, a)
		}
	}
	if len(out.Providers) == 0 && len(out.Groups) == 0 {
		return Manifest{}, ErrSelection
	}
	// All plugin descriptors are required candidates only: no code or options is
	// included, and the destination must explicitly resolve/approve each source.
	return out, Validate(out)
}
func Seal(m Manifest, password string) ([]byte, error) {
	if err := Validate(m); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(m)
	return backup.SealData(raw, password)
}
func Open(data []byte, password string) (Manifest, error) {
	if len(data) > MaxBytes {
		return Manifest{}, ErrInvalid
	}
	raw, err := backup.OpenData(data, password)
	if err != nil {
		return Manifest{}, err
	}
	return Decode(raw)
}
