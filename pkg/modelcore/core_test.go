package modelcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type fixtureClient struct {
	conn      net.Conn
	mu        sync.Mutex
	writeMu   sync.Mutex
	pending   map[int]chan request
	docs      map[string]json.RawMessage
	next      int
	attempts  int
	finished  int
	native    int
	failVault bool
	done      chan struct{}
}

func (f *fixtureClient) write(value any) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	_ = json.NewEncoder(f.conn).Encode(value)
}
func (f *fixtureClient) read() {
	defer close(f.done)
	decoder := json.NewDecoder(f.conn)
	for {
		var message request
		if decoder.Decode(&message) != nil {
			return
		}
		var id string
		if message.Event == "secret" {
			var p struct {
				Ref     string          `json:"ref"`
				Payload json.RawMessage `json:"payload"`
			}
			_ = json.Unmarshal(message.Params, &p)
			_ = json.Unmarshal(message.ID, &id)
			f.mu.Lock()
			value := f.docs[p.Ref]
			failed := f.failVault && message.Method == "secret.set"
			if message.Method == "secret.set" && !failed {
				f.docs[p.Ref] = append(json.RawMessage(nil), p.Payload...)
			}
			f.mu.Unlock()
			if failed {
				f.write(map[string]any{"id": id, "error": RPCError{"VAULT_FAILED", "fixture unavailable"}})
			} else if message.Method == "secret.set" {
				f.write(map[string]any{"id": id, "result": map[string]bool{"ok": true}})
			} else {
				f.write(map[string]any{"id": id, "result": map[string]any{"payload": value}})
			}
			continue
		}
		if message.Event == "admission" {
			var p struct {
				Provider   string `json:"provider_id"`
				Invocation string `json:"invocation"`
			}
			_ = json.Unmarshal(message.Params, &p)
			_ = json.Unmarshal(message.ID, &id)
			f.mu.Lock()
			f.attempts++
			callID := fmt.Sprintf("fixture-call-%d", f.attempts)
			f.mu.Unlock()
			if p.Provider == "denied" || p.Invocation == "denyfixture" {
				f.write(map[string]any{"id": id, "error": RPCError{"DENIED", "not authorized"}})
			} else {
				f.write(map[string]any{"id": id, "result": map[string]any{"ok": true, "call_id": callID}})
			}
			continue
		}
		if message.Event == "native.invoke" {
			_ = json.Unmarshal(message.ID, &id)
			f.mu.Lock()
			f.native++
			f.mu.Unlock()
			data := `data: {"id":"native","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"原生桥已接通"},"finish_reason":null}]}

data: [DONE]

`
			f.write(map[string]any{"id": id, "result": map[string]any{"status": 200, "headers": map[string]string{"Content-Type": "text/event-stream"}}})
			f.write(map[string]any{"id": id, "event": "data", "data": base64.StdEncoding.EncodeToString([]byte(data))})
			f.write(map[string]any{"id": id, "event": "end"})
			continue
		}
		if message.Event == "attempt.finished" {
			f.mu.Lock()
			f.finished++
			f.mu.Unlock()
			continue
		}
		var numeric int
		if json.Unmarshal(message.ID, &numeric) == nil {
			f.mu.Lock()
			ch := f.pending[numeric]
			f.mu.Unlock()
			if ch != nil {
				ch <- message
			}
		}
	}
}
func (f *fixtureClient) call(t *testing.T, method string, params any) request {
	t.Helper()
	f.mu.Lock()
	f.next++
	id := f.next
	ch := make(chan request, 1)
	f.pending[id] = ch
	f.mu.Unlock()
	f.write(map[string]any{"id": id, "method": method, "params": params})
	select {
	case message := <-ch:
		f.mu.Lock()
		delete(f.pending, id)
		f.mu.Unlock()
		return message
	case <-time.After(15 * time.Second):
		t.Fatalf("%s did not finish", method)
		return request{}
	}
}
func result(t *testing.T, message request, target any) {
	t.Helper()
	if message.Error != nil {
		t.Fatalf("core failed: %s", message.Error.Message)
	}
	if err := json.Unmarshal(message.Result, target); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCoreUsesRealGatewayVaultAndFallback(t *testing.T) {
	oauthEntered := make(chan struct{})
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth" {
			close(oauthEntered)
			<-r.Context().Done()
			return
		}
		upstreamCalls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer failure-secret" {
			http.Error(w, `{"error":{"message":"rate limited"}}`, 429)
			return
		}
		if r.Header.Get("Authorization") != "Bearer successful-secret" {
			t.Errorf("unexpected upstream authentication")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"reply\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"真实网关已接通\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5,\"total_tokens\":8}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	parent, child := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- Serve(ctx, child, child); child.Close() }()
	client := &fixtureClient{conn: parent, pending: map[int]chan request{}, docs: map[string]json.RawMessage{}, done: make(chan struct{})}
	go client.read()
	defer func() {
		parent.Close()
		select {
		case <-serverDone:
		case <-time.After(25 * time.Second):
			t.Error("managed core did not stop on EOF")
		}
		<-client.done
	}()
	root := t.TempDir()
	claude := fixtureOfficialClaude(t, root)
	bun, _ := exec.LookPath("bun")
	plugins := []plugin.Entry{}
	if bun != "" {
		file := filepath.Join(root, "fixture.js")
		source := fmt.Sprintf(`export default async ({client}) => ({
 config(c) {
  c.provider.fixture = {name:"Managed fixture",npm:"@ai-sdk/openai-compatible",api:%q,models:{"fixture-model":{name:"Fixture"}}}
 },
 auth: {
  provider:"fixture", methods:[{type:"api",label:"API key"},{type:"oauth",label:"Browser",authorize:async()=>({method:"code",url:"https://fixture.invalid/login",callback:async()=>{await fetch(%q);return {type:"success",refresh:"late-refresh",access:"late-access",expires:Date.now()+3600000}}})}],
  async loader(get) {
   const auth = await get()
   return {
    apiKey:"dummy", baseURL:%q,
    fetch:async(url,init) => {
     await client.auth.set({path:{id:"fixture"},body:{...auth,renewed:true}})
     return fetch(url,{...init,headers:{...init.headers,Authorization:"Bearer "+auth.key}})
    }
   }
  }
 }
})`, upstream.URL+"/v1", upstream.URL+"/oauth", upstream.URL+"/v1")
		if err := os.WriteFile(file, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		plugins = append(plugins, plugin.Entry{Spec: file})
	}
	var ready struct {
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
		OwnerID int64  `json:"owner_id"`
	}
	result(t, client.call(t, "init", map[string]any{"protocol": 1, "owner_id": 7, "data_dir": filepath.Join(root, "config"), "cache_dir": filepath.Join(root, "cache"), "bun_path": bun, "claude_path": claude, "plugins": plugins, "providers": []any{}, "groups": []any{}}), &ready)
	if ready.OwnerID != 7 || ready.Token == "" {
		t.Fatal("missing fixed owner or inference bearer")
	}
	var sources struct {
		Presets []provider.PresetDef `json:"presets"`
	}
	result(t, client.call(t, "sources", nil), &sources)
	if len(sources.Presets) < 57 {
		t.Fatalf("missing provider catalog: %d", len(sources.Presets))
	}
	for id, key := range map[string]string{"first": "failure-secret", "second": "successful-secret", "denied": "successful-secret"} {
		result(t, client.call(t, "provider.save", map[string]any{"provider": provider.Provider{ID: id, Name: id, Chat: upstream.URL + "/v1", Key: key, Models: []string{"fixture-model"}}}), new(map[string]any))
	}
	if reply := client.call(t, "provider.save", map[string]any{"provider": provider.Provider{ID: "host-adc", Preset: provider.VertexPreset, Vertex: &provider.Vertex{Project: "fixture-project", Location: "global", Credentials: filepath.Join(root, "host-credentials.json")}}}); reply.Error == nil {
		t.Fatal("managed Vertex accepted host credentials path")
	}
	if reply := client.call(t, "provider.save", map[string]any{"provider": provider.Provider{ID: "host-adc", Preset: provider.VertexPreset, Vertex: &provider.Vertex{Project: "fixture-project", Location: "global"}}}); reply.Error == nil {
		t.Fatal("managed Vertex accepted host ADC fallback")
	}
	public := client.call(t, "providers.list", nil)
	if strings.Contains(string(public.Result), "successful-secret") || strings.Contains(string(public.Result), "failure-secret") {
		t.Fatal("public catalog exposed credentials")
	}
	if _, err := os.Stat(filepath.Join(root, "config", "providers.json")); !os.IsNotExist(err) {
		t.Fatal("managed providers were persisted outside parent's vault")
	}
	result(t, client.call(t, "group.save", map[string]any{"group": provider.Group{ID: "test", Name: "Test", Members: []string{"first/fixture-model", "second/fixture-model"}, Routing: "order", Patience: provider.PatienceOff}}), new(provider.Group))
	ask := func(model, token, ticket string) (int, string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, ready.BaseURL+"/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":true}`, model)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		if ticket != "" {
			request.Header.Set("X-Xfriend-Invocation", ticket)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body)
	}
	if status, _ := ask("second/fixture-model", "wrong", "fixture"); status != 401 {
		t.Fatalf("unauthenticated gateway status %d", status)
	}
	if status, _ := ask("second/fixture-model", ready.Token, ""); status != 403 {
		t.Fatalf("unscoped inference status %d", status)
	}
	status, body := ask("group/test", ready.Token, "fixture")
	if status != 200 || !strings.Contains(body, "真实网关已接通") {
		t.Fatalf("real routed inference failed (%d): %s", status, body)
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("fallback did not try both providers: %d", upstreamCalls.Load())
	}
	before := upstreamCalls.Load()
	status, body = ask("denied/fixture-model", ready.Token, "denyfixture")
	if status != 403 || !strings.Contains(body, "INVOCATION_DENIED") || strings.Contains(body, "真实网关已接通") || upstreamCalls.Load() != before {
		t.Fatalf("attempt denied by parent still reached upstream: status=%d, calls=%d, before=%d", status, upstreamCalls.Load(), before)
	}
	result(t, client.call(t, "provider.save", map[string]any{"provider": provider.Provider{ID: "native-fixture", Preset: "xfriend-native", Models: []string{"fixture-model"}}}), new(map[string]any))
	status, body = ask("native-fixture/fixture-model", ready.Token, "native-ticket")
	if status != 200 || !strings.Contains(body, "原生桥已接通") {
		t.Fatalf("native bridge failed %d: %s", status, body)
	}
	if upstreamCalls.Load() != before {
		t.Fatal("native bridge leaked request to network")
	}
	testManagedGoogle(t, client, ask, ready.Token)
	if claude != "" {
		testManagedClaude(t, client, ask, ready.Token)
	}
	if bun != "" {
		result(t, client.call(t, "plugin.list", nil), new([]plugin.Provider))
		var saved plugin.Saved
		result(t, client.call(t, "auth.key", map[string]any{"source_id": "fixture", "method": 0, "key": "successful-secret", "account_id": "new"}), &saved)
		result(t, client.call(t, "plugin.list", nil), new([]plugin.Provider))
		result(t, client.call(t, "provider.bind", provider.ManagedBinding{ID: "plugin-fixture", SourceID: "fixture", AccountID: saved.Account}), new(map[string]any))
		status, body = ask("plugin-fixture/fixture-model", ready.Token, "plugin-ticket")
		if status != 200 || !strings.Contains(body, "真实网关已接通") {
			t.Fatalf("actual Bun plugin gateway failed %d: %s", status, body)
		}
		client.mu.Lock()
		authDoc := string(client.docs["modelcore/plugin-auth"])
		client.mu.Unlock()
		if !strings.Contains(authDoc, "renewed") {
			t.Fatal("plugin refresh was not durably persisted")
		}
		if _, err := os.Stat(filepath.Join(root, "config", "plugin-auth.json")); !os.IsNotExist(err) {
			t.Fatal("plugin auth escaped vault")
		}
		var authorization plugin.Authorization
		result(t, client.call(t, "auth.start", map[string]any{"source_id": "fixture", "method": 1, "account_id": "new"}), &authorization)
		finishReply := make(chan request, 1)
		go func() {
			finishReply <- client.call(t, "auth.finish", map[string]any{"session": authorization.Session, "code": "fixture"})
		}()
		select {
		case <-oauthEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("OAuth callback did not start")
		}
		result(t, client.call(t, "auth.cancel", map[string]any{"session": authorization.Session}), new(map[string]any))
		select {
		case reply := <-finishReply:
			if reply.Error == nil {
				t.Fatal("cancelled OAuth callback confirmed sign-in")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("OAuth cancellation did not abort fetch")
		}
		client.mu.Lock()
		cancelledDoc := string(client.docs["modelcore/plugin-auth"])
		client.mu.Unlock()
		if strings.Contains(cancelledDoc, "late-refresh") {
			t.Fatal("cancelled OAuth callback persisted credentials")
		}
		client.mu.Lock()
		client.failVault = true
		client.mu.Unlock()
		if response := client.call(t, "auth.key", map[string]any{"source_id": "fixture", "method": 0, "key": "not-persisted", "account_id": "new"}); response.Error == nil {
			t.Fatal("plugin auth confirmed before durable vault ACK")
		}
		client.mu.Lock()
		client.failVault = false
		client.mu.Unlock()
	}
	if response := client.call(t, "sources", map[string]any{"owner_id": 8}); response.Error == nil {
		t.Fatal("owner switch accepted")
	}
	client.mu.Lock()
	attempts, finished := client.attempts, client.finished
	kept := string(client.docs["modelcore/providers"])
	client.mu.Unlock()
	if attempts < 3 || finished < 2 {
		t.Fatalf("attempt admission/settlement incomplete: %d/%d", attempts, finished)
	}
	if !strings.Contains(kept, "successful-secret") {
		t.Fatal("provider credentials were not durably put in parent vault")
	}
	var trace map[string]any
	result(t, client.call(t, "trace", nil), &trace)
	if strings.Contains(string(client.call(t, "trace", nil).Result), "hello") {
		t.Fatal("routing record included prompt text")
	}
}
