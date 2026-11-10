package gui

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/yetone/magpie/internal/plugin"
)

type oneTokenKey struct {
	Account string `json:"account"`
	Key     string `json:"key"`
}

// oneTokenKeyRoutes lets the local account page copy a plugin's key. The
// gateway's own API never exposes a plugin's credential; this route is
// loopback-only and returns it only when the reader clicks Copy.
func oneTokenKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/plugin-key", func(rw http.ResponseWriter, r *http.Request) {
		if !loopbackPeer(r.RemoteAddr) {
			http.Error(rw, "only this computer may read a plugin key", http.StatusForbidden)
			return
		}
		var in struct {
			Provider string `json:"provider"`
		}
		r.Body = http.MaxBytesReader(rw, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Provider == "" {
			http.Error(rw, "a provider is needed", http.StatusBadRequest)
			return
		}
		out := []oneTokenKey{}
		for account, auth := range plugin.Auths(in.Provider) {
			key, _ := auth["key"].(string)
			if key == "" {
				continue
			}
			out = append(out, oneTokenKey{Account: account, Key: key})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{"keys": out})
	})
}
