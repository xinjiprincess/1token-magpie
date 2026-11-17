package modeltransfer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestExplicitAppTransferReadsOnlyKeysAndDetectsSourceChanges(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	path := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"openai":{"type":"api","key":"fixture-app-key"},"opencode-go":{"type":"oauth","refresh":"never-import-login"}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	m, ignored, err := CollectApp("opencode", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Providers) != 1 || len(ignored) != 1 {
		t.Fatalf("providers=%d ignored=%d", len(m.Providers), len(ignored))
	}
	p, err := Inspect(m)
	if err != nil {
		t.Fatal(err)
	}
	preview, _ := json.Marshal(p)
	manifest, _ := json.Marshal(m)
	if bytes.Contains(preview, []byte("fixture-app-key")) || bytes.Contains(manifest, []byte("never-import-login")) {
		t.Fatal("external app preview or manifest leaked credentials")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Fatal("source configuration was modified")
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(original, []byte("fixture-app-key"), []byte("changed-app-key")), 0600); err != nil {
		t.Fatal(err)
	}
	changed, _, err := CollectApp("opencode", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(changed, Selection{Fingerprint: p.Fingerprint, ProviderIDs: []string{m.Providers[0].ID}, IncludeKeys: true}); err != ErrChanged {
		t.Fatal("changed app credentials were silently imported")
	}
	if _, _, err := CollectApp("unsupported", "fixture"); err == nil {
		t.Fatal("unknown source accepted")
	}
}
