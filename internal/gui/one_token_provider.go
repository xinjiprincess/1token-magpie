package gui

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

var oneTokenProviderOnce sync.Once

// InstallOneTokenProvider installs the provider folder shipped beside the
// exe when the app is opened directly, so no Start.cmd is needed. It leaves
// an existing 1token plugin alone and never blocks the UI on an error.
func InstallOneTokenProvider() {
	oneTokenProviderOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		dir := filepath.Join(filepath.Dir(exe), "provider")
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
			return
		}
		installed := false
		for _, p := range plugin.Load().Plugins {
			if p.Spec == dir || plugin.Name(p.Spec) == "1_token_magpie" {
				installed = true
				break
			}
		}
		if !installed {
			if _, err := provider.AddPlugin(context.Background(), dir); err != nil {
				return
			}
		}
		if plugin.Load().Prefer["1token"] != dir {
			_ = plugin.Prefer(dir, "1token")
		}
	})
}
