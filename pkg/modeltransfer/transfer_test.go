package modeltransfer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }
func source(t *testing.T) Manifest {
	t.Helper()
	testenv.SetHome(t, t.TempDir())
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("providers.json", `{"providers":[{"id":"opencode-go","name":"Go account","preset":"opencode-go","key":"secret-main","keys":[{"name":"backup","key":"secret-backup"}],"chat":"https://opencode.example/v1","responses":"https://opencode.example/v1","models":["model-a"],"keepLogin":true,"keepLoginAs":"do-not-carry","proxy":"http://u:p@proxy.invalid:8080"},{"id":"relay","name":"Other relay","key":"other-secret","chat":"https://relay.invalid/v1"}],"groups":[{"id":"daily","name":"Daily","members":["opencode-go/model-a","relay/other"],"routing":"smart"}]}`)
	write("plugins.json", `{"plugins":[{"spec":"@magpie-community/opencode-commandcode-auth@0.1.21","options":{"token":"option-secret"}},{"spec":"/private/plugin.js"}],"config":{"secret":"never-export"}}`)
	write("logins.json", `[{"id":"saved-codex","agent":"codex","user":"someone@example.test","plan":"Plus","auth":{"refresh_token":"never-copy-oauth"}}]`)
	write("plugin-auth.json", `{"commandcode-plan#work":{"type":"oauth","email":"employee@example.test","refresh":"plugin-token-never-copy","cli":true}}`)
	m, err := Collect("fixture-version")
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestCollectPreviewNeverDisclosesCredentialsOrChangesSources(t *testing.T) {
	m := source(t)
	before, err := os.ReadFile(provider.Path())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := Inspect(m)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(preview)
	for _, secret := range []string{"secret-main", "secret-backup", "other-secret", "plugin-token-never-copy", "never-copy-oauth", "option-secret", "someone@example.test"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("preview contains secret %q", secret)
		}
	}
	manifest, _ := json.Marshal(m)
	for _, secret := range []string{"never-copy-oauth", "plugin-token-never-copy", "option-secret", "do-not-carry", "/private/plugin.js"} {
		if bytes.Contains(manifest, []byte(secret)) {
			t.Fatalf("model manifest contains forbidden %q", secret)
		}
	}
	if len(m.Accounts) != 2 || len(m.Plugins) != 1 {
		t.Fatalf("accounts=%d plugins=%d", len(m.Accounts), len(m.Plugins))
	}
	for _, a := range m.Accounts {
		if a.AuthState != "needs_reauth" || a.TransferSupported {
			t.Fatal("subscription must require reauthorization")
		}
	}
	after, _ := os.ReadFile(provider.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed source")
	}
}

func TestVertexCredentialsNeverTransferAndRequireReauthentication(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"providers":[{"id":"vertex","name":"Vertex","preset":"google-vertex","vertex":{"project":"fixture","credentials":"/fixture/adc.json","credentialsJSON":"{\"refresh_token\":\"private-adc-token\"}"}}]}`
	if err := os.WriteFile(provider.Path(), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Collect("fixture")
	if err != nil {
		t.Fatal(err)
	}
	vertexState := ""
	for _, p := range m.Providers {
		if p.ID == "vertex" {
			vertexState = p.AuthState
		}
	}
	if vertexState != "needs_reauth" {
		states := []string{}
		for _, p := range m.Providers {
			states = append(states, p.ID+":"+p.Preset+":"+p.AuthState)
		}
		t.Fatalf("Vertex must require reauthentication: %v", states)
	}
	encoded, _ := json.Marshal(m)
	for _, secret := range []string{"private-adc-token", "credentialsJSON", "/fixture/adc.json"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("Vertex credential transferred: %s", secret)
		}
	}
}
func TestSelectedKeysAndSourceFingerprint(t *testing.T) {
	m := source(t)
	preview, _ := Inspect(m)
	selection := Selection{Fingerprint: preview.Fingerprint, ProviderIDs: []string{"opencode-go"}, GroupIDs: []string{"daily"}, IncludeKeys: true}
	out, err := Select(m, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Providers) != 1 || out.Providers[0].Config["key"] != "secret-main" || len(out.Groups) != 1 {
		t.Fatal("selected provider/key/group missing")
	}
	data, _ := json.Marshal(out)
	if bytes.Contains(data, []byte("other-secret")) {
		t.Fatal("unselected key exported")
	}
	selection.IncludeKeys = false
	keyless, err := Select(m, selection)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(keyless)
	for _, secret := range []string{"secret-main", "secret-backup", "u:p@"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("keyless export contains credentials")
		}
	}
	if m.Providers[0].Config["key"] != "secret-main" {
		t.Fatal("selection mutated original source")
	}
	changed := m
	changed.Providers = append([]Provider{}, m.Providers...)
	changed.Providers[0].Config = map[string]any{"id": "opencode-go", "key": "rotated-key"}
	if _, err := Select(changed, selection); err != ErrChanged {
		t.Fatalf("changed source accepted: %v", err)
	}
	selection.ProviderIDs = []string{"missing"}
	if _, err := Select(m, selection); err != ErrSelection {
		t.Fatalf("missing selection: %v", err)
	}
}
func TestEncryptedFileRoundTripRejectsPasswordTamperingAndWrongPurpose(t *testing.T) {
	m := source(t)
	preview, _ := Inspect(m)
	chosen, err := Select(m, Selection{Fingerprint: preview.Fingerprint, ProviderIDs: []string{"opencode-go"}, IncludeKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(chosen, "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("secret-main")) {
		t.Fatal("plaintext key in file")
	}
	opened, err := Open(sealed, "test-passphrase")
	if err != nil || opened.Providers[0].Config["key"] != "secret-main" {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err := Open(sealed, "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	var document map[string]any
	_ = json.Unmarshal(sealed, &document)
	document["data"] = "AAAA"
	corrupt, _ := json.Marshal(document)
	if _, err := Open(corrupt, "test-passphrase"); err == nil {
		t.Fatal("tampered file accepted")
	}
	chosen.Purpose = "magpie-backup"
	if _, err := Seal(chosen, "password"); err == nil {
		t.Fatal("general backup accepted")
	}
	raw, _ := json.Marshal(m)
	bad := strings.Replace(string(raw), `"purpose":"xfriend-model-transfer"`, `"purpose":"xfriend-model-transfer","agents":{}`, 1)
	if _, err := Decode([]byte(bad)); err == nil {
		t.Fatal("unknown code/config-bearing field accepted")
	}
}
func TestMalformedSourceFailsInsteadOfExportingEmpty(t *testing.T) {
	source(t)
	if err := os.WriteFile(filepath.Join(settings.Dir(), "plugins.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect("test"); err == nil {
		t.Fatal("unreadable plugin source treated as empty")
	}
}
func TestStdioListAndExportAreExplicitAndRedacted(t *testing.T) {
	m := source(t)
	preview, _ := Inspect(m)
	selection, _ := json.Marshal(Selection{Fingerprint: preview.Fingerprint, ProviderIDs: []string{"opencode-go"}, IncludeKeys: true})
	input := `{"id":1,"method":"list"}` + "\n" + `{"id":2,"method":"export","params":` + string(selection) + "}\n"
	var out bytes.Buffer
	if err := Serve(strings.NewReader(input), &out, "fixture-version"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("wrong RPC response count")
	}
	if strings.Contains(lines[0], "secret-main") || !strings.Contains(lines[1], "secret-main") {
		t.Fatal("list/export secret boundary incorrect")
	}
	if !strings.Contains(lines[1], `"id":2`) {
		t.Fatal("RPC response lost request id")
	}
}

func TestManifestRejectsExecutableOrCredentialBearingPluginDescriptors(t *testing.T) {
	m := source(t)
	for _, spec := range []string{"/private/custom.js", "git+file:///private/repo", "git+https://token:secret@github.com/owner/repo", "javascript:alert(1)"} {
		changed := m
		changed.Plugins = []Plugin{{Spec: spec}}
		if err := Validate(changed); err == nil {
			t.Fatalf("unsafe plugin accepted: %s", spec)
		}
	}
	m.Providers[0].Config["chat"] = "https://user:pass@relay.example/v1"
	m.Providers[0].Config["extraBody"] = map[string]any{"api_key": "body-secret"}
	preview, _ := Inspect(m)
	selected, err := Select(m, Selection{Fingerprint: preview.Fingerprint, ProviderIDs: []string{"opencode-go"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(selected)
	if bytes.Contains(raw, []byte("user:pass")) || bytes.Contains(raw, []byte("body-secret")) {
		t.Fatal("keyless export leaked URL or body credentials")
	}
}
