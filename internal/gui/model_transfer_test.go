package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/pkg/modeltransfer"
)

type transferWindow struct {
	Windows
	opened []string
}

func (w *transferWindow) OpenURL(value string) error { w.opened = append(w.opened, value); return nil }
func (w *transferWindow) OpenFolder(string) error    { return nil }

func TestModelTransferPreviewExportAndFixedDeepLink(t *testing.T) {
	home := sandboxHome(t)
	if err := os.MkdirAll(filepath.Dir(provider.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `{"providers":[{"id":"opencode-go","name":"Go","key":"sensitive-key","chat":"https://go.example/v1","models":["a"]}],"groups":[]}`
	if err := os.WriteFile(provider.Path(), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	window := &transferWindow{}
	mux := http.NewServeMux()
	modelTransferRoutes(mux, window)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	response := request("GET", "/api/model-transfer/preview", nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), "sensitive-key") {
		t.Fatalf("preview: %d %s", response.Code, response.Body)
	}
	var preview modeltransfer.Preview
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	pick := modeltransfer.Selection{Fingerprint: preview.Fingerprint, ProviderIDs: []string{"opencode-go"}, IncludeKeys: true}
	response = request("POST", "/api/model-transfer/export", map[string]any{"selection": pick, "password": "test-password"})
	if response.Code != 200 {
		t.Fatalf("export: %d %s", response.Code, response.Body)
	}
	var result struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	filename := strings.Replace(result.Path, "~", home, 1)
	sealed, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "sensitive-key") {
		t.Fatal("export file contains plaintext key")
	}
	manifest, err := modeltransfer.Open(sealed, "test-password")
	if err != nil || manifest.Providers[0].Config["key"] != "sensitive-key" {
		t.Fatalf("export payload: %v", err)
	}
	response = request("POST", "/api/model-transfer/export", map[string]any{"selection": pick, "password": "test-password"})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var second struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &second)
	if result.Path == second.Path {
		t.Fatal("export overwrote existing file")
	}
	pick.Fingerprint = "stale"
	response = request("POST", "/api/model-transfer/export", map[string]any{"selection": pick, "password": "test-password"})
	if response.Code == 200 {
		t.Fatal("stale source exported")
	}
	response = request("POST", "/api/model-transfer/open-xfriend", map[string]string{"url": "https://evil.example/?secret=leak"})
	if response.Code != 200 || len(window.opened) != 1 || window.opened[0] != "xfriend://models/import?source=magpie" {
		t.Fatal("deep link was not fixed and credential-free")
	}
	after, _ := os.ReadFile(provider.Path())
	if string(after) != original {
		t.Fatal("model export modified source settings")
	}
}
