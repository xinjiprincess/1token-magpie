package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/appdir"
)

var managedRuntime struct {
	sync.RWMutex
	bun  string
	list *List
}

// ConfigureManagedRuntime selects the locked runtime and already installed
// plugin paths. Managed mode never downloads a runtime or package on first use.
func ConfigureManagedRuntime(bun string, entries []Entry) error {
	if bun != "" {
		if !filepath.IsAbs(bun) {
			return errors.New("bundled Bun path must be absolute")
		}
		if info, err := os.Stat(bun); err != nil || info.IsDir() {
			return errors.New("bundled Bun is missing")
		}
	}
	for _, entry := range entries {
		if !filepath.IsAbs(entry.Spec) {
			return errors.New("managed plugins must name locked installed paths")
		}
		if _, err := os.Stat(entry.Spec); err != nil {
			return errors.New("bundled plugin is missing")
		}
	}
	managedRuntime.Lock()
	defer managedRuntime.Unlock()
	if managedRuntime.list != nil {
		return errors.New("managed plugin runtime already configured")
	}
	managedRuntime.bun = bun
	managedRuntime.list = &List{Plugins: clonePlugins(entries)}
	return nil
}
func managedBun() (string, bool) {
	managedRuntime.RLock()
	defer managedRuntime.RUnlock()
	return managedRuntime.bun, managedRuntime.list != nil
}
func managedList() (List, bool) {
	managedRuntime.RLock()
	defer managedRuntime.RUnlock()
	if managedRuntime.list == nil {
		return List{}, false
	}
	return List{Plugins: clonePlugins(managedRuntime.list.Plugins)}, true
}

// Shutdown stops only this process's active and retiring plugin hosts, waiting
// through their existing refresh-save window before killing an unresponsive one.
func Shutdown(ctx context.Context) error {
	provMu.Lock()
	provSettling = true
	provMu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshing.Wait()
		Told()
		hostMu.Lock()
		hosts := make([]*host, 0, len(retiring)+1)
		if current != nil {
			hosts = append(hosts, current)
			current = nil
		}
		for h := range retiring {
			hosts = append(hosts, h)
		}
		hostMu.Unlock()
		for _, h := range hosts {
			h.stop()
		}
		Told()
		refreshing.Wait()
		cancelCatalogSyncs()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// managedEnvironment keeps trusted provider plugins away from the host user's
// CLI account directories. The bundled runtime receives only its owner's home.
func ManagedEnvironment(env []string) []string {
	if !appdir.Managed() {
		return env
	}
	home := filepath.Join(appdir.Config(), "plugin-home")
	paths := map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME":       filepath.Join(home, "config"),
		"XDG_DATA_HOME":         filepath.Join(home, "data"),
		"XDG_STATE_HOME":        filepath.Join(home, "state"),
		"XDG_CACHE_HOME":        filepath.Join(appdir.Cache(), "plugin-home"),
		"APPDATA":               filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA":          filepath.Join(home, "AppData", "Local"),
		"BUN_INSTALL_CACHE_DIR": filepath.Join(appdir.Cache(), "bun"),
	}
	allowed := map[string]bool{"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true, "TMPDIR": true, "TMP": true, "TEMP": true, "MAGPIE_HTTP_PROXY": true, "MAGPIE_HTTPS_PROXY": true, "MAGPIE_ALL_PROXY": true, "MAGPIE_NO_PROXY": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true}
	out := make([]string, 0, len(env)+len(paths))
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if allowed[strings.ToUpper(name)] {
			out = append(out, item)
		}
	}
	for name, value := range paths {
		out = append(out, name+"="+value)
	}
	return out
}

// ManagedMethodAllowed rejects sign-ins whose advertised purpose is reading or
// invoking another local CLI. Browser/device OAuth and manual keys remain usable.
func ManagedMethodAllowed(m Method) bool {
	label := strings.ToLower(m.Label)
	for _, word := range []string{"cli", "local credential", "existing login", "current sign-in", "desktop", "app's sign-in", "-agent's sign-in", "import cpa", "本机", "本地登录", "已有登录"} {
		if strings.Contains(label, word) {
			return false
		}
	}
	return m.Type == "api" || m.Type == "oauth"
}

// CancelAuthorization makes a late OAuth callback unable to save credentials.
func CancelAuthorization(ctx context.Context, session string) error {
	return Call(ctx, "cancelAuthorization", map[string]any{"session": session}, nil)
}
