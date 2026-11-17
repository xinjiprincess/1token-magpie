package modelcore

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

func testManagedGoogle(t *testing.T, client *fixtureClient, ask func(string, string, string) (int, string), token string) {
	t.Helper()
	var refreshes atomic.Int32
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshes.Add(1)
				fmt.Fprint(w, `{"access_token":"fresh-google-access","expires_in":3600}`)
			} else {
				fmt.Fprint(w, `{"access_token":"oauth-google-access","refresh_token":"owner-google-refresh","expires_in":3600}`)
			}
		case r.URL.Path == "/user":
			fmt.Fprint(w, `{"email":"owner@example.invalid"}`)
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			fmt.Fprint(w, `{"cloudaicompanionProject":"owned-project","currentTier":{"id":"paid-tier","name":"Pro"}}`)
		case strings.HasSuffix(r.URL.Path, ":streamGenerateContent") || strings.HasSuffix(r.URL.Path, ":generateContent"):
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Google原生接通\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2,\"totalTokenCount\":5}}}\n\n")
		default:
			http.Error(w, "unexpected native endpoint", 404)
		}
	}))
	defer vendor.Close()
	defer provider.ManagedGoogleForTest(vendor.URL+"/authorize", vendor.URL+"/token", vendor.URL+"/user", vendor.URL+"/codeassist")()
	var authorization plugin.Authorization
	result(t, client.call(t, "auth.start", map[string]any{"source_id": "antigravity", "method": 0}), &authorization)
	link, err := url.Parse(authorization.URL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(link.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	query := callback.Query()
	query.Set("state", link.Query().Get("state"))
	query.Set("code", "fixture-code")
	callback.RawQuery = query.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	var saved plugin.Saved
	result(t, client.call(t, "auth.finish", map[string]any{"session": authorization.Session}), &saved)
	result(t, client.call(t, "provider.bind", provider.ManagedBinding{ID: "google-oauth", SourceID: "antigravity", AccountID: saved.Account}), new(map[string]any))
	status, body := ask("google-oauth/gemini-3.5-flash", token, "native-google")
	if status != 200 || !strings.Contains(body, "Google原生接通") {
		t.Fatalf("native Google OAuth inference=%d %s", status, body)
	}
	raw := `{"access_token":"expired-google-access","refresh_token":"owner-imported-refresh","expiry_date":1,"project":"owned-project"}`
	result(t, client.call(t, "auth.key", map[string]any{"source_id": "antigravity", "method": 1, "key": raw}), &saved)
	result(t, client.call(t, "provider.bind", provider.ManagedBinding{ID: "google-refresh", SourceID: "antigravity", AccountID: saved.Account}), new(map[string]any))
	status, body = ask("google-refresh/gemini-3.5-flash", token, "native-google-refresh")
	if status != 200 || !strings.Contains(body, "Google原生接通") || refreshes.Load() != 1 {
		t.Fatalf("native Google refresh=%d %s count=%d", status, body, refreshes.Load())
	}
	client.mu.Lock()
	kept := string(client.docs["modelcore/native-auth"])
	client.mu.Unlock()
	if !strings.Contains(kept, "fresh-google-access") {
		t.Fatal("Google token refresh returned before durable vault ACK")
	}
}

func fixtureOfficialClaude(t *testing.T, root string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return ""
	}
	path := filepath.Join(root, "official-claude-fixture")
	script := `#!/bin/sh
if [ "$1" = "setup-token" ]; then
 echo 'https://claude.example.invalid/authorize'
 read -r code
 echo 'sk-ant-oat01-owner-setup-fixture'
 exit 0
fi
if [ "$CLAUDE_CODE_OAUTH_TOKEN" != "sk-ant-oat01-owner-setup-fixture" ]; then exit 7; fi
while read -r line; do
 echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":4}}}}'
 echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Claude官方桥接通"}}}'
 echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}'
 echo '{"type":"stream_event","event":{"type":"message_stop"}}'
 echo '{"type":"result","subtype":"success","is_error":false,"result":""}'
done
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func testManagedClaude(t *testing.T, client *fixtureClient, ask func(string, string, string) (int, string), token string) {
	t.Helper()
	var authorization plugin.Authorization
	result(t, client.call(t, "auth.start", map[string]any{"source_id": "claude", "method": 0}), &authorization)
	if !strings.HasPrefix(authorization.URL, "https://claude.example.invalid/") {
		t.Fatalf("not official CLI flow: %+v", authorization)
	}
	var saved plugin.Saved
	result(t, client.call(t, "auth.finish", map[string]any{"session": authorization.Session, "code": "authorized"}), &saved)
	result(t, client.call(t, "provider.bind", provider.ManagedBinding{ID: "claude-owned", SourceID: "claude", AccountID: saved.Account}), new(map[string]any))
	status, body := ask("claude-owned/claude-sonnet-5", token, "native-claude")
	if status != 200 || !strings.Contains(body, "Claude官方桥接通") {
		t.Fatalf("Claude subscription bridge=%d %s", status, body)
	}
	public := client.call(t, "providers.list", nil)
	if strings.Contains(string(public.Result), "sk-ant-oat01") {
		t.Fatal("native provider catalog leaked setup-token")
	}
	var quota map[string]any
	result(t, client.call(t, "provider.quota", map[string]any{"id": "claude-owned"}), &quota)
	if quota["state"] != "unknown" {
		t.Fatal("setup-token claimed unsupported subscription quota")
	}
	// A valid token is never put on an arbitrary credential file by this flow.
}
