package modeltransfer

import (
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type IgnoredAppEntry struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// CollectApp is used only by the standalone, explicitly invoked source helper.
// Managed cores never call it. Client sign-ins are skipped by the stock reader.
func CollectApp(sourceID, appVersion string) (Manifest, []IgnoredAppEntry, error) {
	source, err := provider.ReadModelAppTransfer(sourceID)
	if err != nil {
		return Manifest{}, nil, ErrInvalid
	}
	path, err := filepath.Abs(source.Path)
	if err != nil {
		return Manifest{}, nil, ErrInvalid
	}
	m := Manifest{Purpose: Purpose, Version: Version, SourceID: hash([]byte(sourceID + "\x00" + path)), CreatedAt: time.Now().Unix(), AppVersion: appVersion,
		Providers: []Provider{}, Accounts: []Account{}, Groups: []provider.Group{}, Plugins: []Plugin{}}
	ignored := []IgnoredAppEntry{}
	for _, item := range source.Items {
		p := item.Provider
		if item.Skip != "" || p.Key == "" {
			ignored = append(ignored, IgnoredAppEntry{Name: p.Name, Reason: "needs_reauth_or_unsupported"})
			continue
		}
		p.ID = sourceID + "-" + hash([]byte(item.Ref))[:16]
		m.Providers = append(m.Providers, Provider{ID: p.ID, Name: p.Name, Preset: p.Preset, Kind: "api", AuthState: "ready", Config: config(p)})
	}
	return m, ignored, Validate(m)
}
