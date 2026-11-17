package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/pkg/modeltransfer"
)

// Model-transfer routes intentionally do not use full backup collection or
// restore: neither the preview nor export discovers or refreshes subscriptions.
func modelTransferRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/model-transfer/preview", func(rw http.ResponseWriter, r *http.Request) {
		manifest, err := modeltransfer.Collect(Version)
		if err != nil {
			fail(rw, err)
			return
		}
		preview, err := modeltransfer.Inspect(manifest)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, preview)
	})
	mux.HandleFunc("POST /api/model-transfer/open-xfriend", func(rw http.ResponseWriter, r *http.Request) {
		// Fixed purpose-only deep link. No URL, key or handoff bearer is supplied by
		// the page; Xfriend authenticates and confirms its own import session.
		if err := w.OpenURL("xfriend://models/import?source=magpie"); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]bool{"opened": true})
	})
	mux.HandleFunc("POST /api/model-transfer/export", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Selection modeltransfer.Selection `json:"selection"`
			Password  string                  `json:"password"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(rw, r.Body, modeltransfer.MaxBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil {
			fail(rw, modeltransfer.ErrInvalid)
			return
		}
		manifest, err := modeltransfer.Collect(Version)
		if err != nil {
			fail(rw, err)
			return
		}
		selected, err := modeltransfer.Select(manifest, in.Selection)
		if err != nil {
			fail(rw, err)
			return
		}
		sealed, err := modeltransfer.Seal(selected, in.Password)
		if err != nil {
			fail(rw, err)
			return
		}
		dir := downloads()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fail(rw, err)
			return
		}
		var target string
		for index := 1; ; index++ {
			suffix := ""
			if index > 1 {
				suffix = fmt.Sprintf("-%d", index)
			}
			target = filepath.Join(dir, "xfriend-models-"+time.Now().Format("2006-01-02")+suffix+".xfriend-models")
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				fail(rw, err)
				return
			}
			_, writeErr := file.Write(sealed)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(target)
				fail(rw, fmt.Errorf("model export could not be saved"))
				return
			}
			break
		}
		_ = w.OpenFolder(dir)
		writeJSON(rw, map[string]any{"path": tilde(target), "providers": len(selected.Providers), "groups": len(selected.Groups), "subscriptions_need_reauth": true})
	})
}
