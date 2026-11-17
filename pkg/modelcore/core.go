// Package modelcore exposes Magpie's model data plane to an owner-bound host.
// It deliberately does not import CLI/GUI or run the standalone daemon loops.
package modelcore

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/managedstate"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/routes"
	"github.com/yetone/magpie/pkg/modeltransfer"
)

const Protocol = 1
const MaxFrame = 48 << 20

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type request struct {
	Event  string          `json:"event"`
	Data   string          `json:"data"`
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}
type Core struct {
	ctx        context.Context
	cancel     context.CancelFunc
	output     io.Writer
	writeMu    sync.Mutex
	mutationMu sync.Mutex
	mu         sync.Mutex
	started    bool
	ready      bool
	closing    bool
	owner      int64
	seq        atomic.Int64
	callbacks  map[string]chan request
	operations map[string]context.CancelFunc
	workers    sync.WaitGroup
	server     *http.Server
	gateway    *gateway.Server
	baseURL    string
	token      string
}

type Init struct {
	Protocol   int                 `json:"protocol"`
	OwnerID    int64               `json:"owner_id"`
	DataDir    string              `json:"data_dir"`
	CacheDir   string              `json:"cache_dir"`
	ClaudePath string              `json:"claude_path"`
	BunPath    string              `json:"bun_path"`
	Plugins    []plugin.Entry      `json:"plugins,omitempty"`
	Providers  []provider.Provider `json:"providers,omitempty"`
	Groups     []provider.Group    `json:"groups,omitempty"`
}

// Serve owns one process's context; EOF stops its listener and only its hosts.
func Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	core := &Core{ctx: ctx, cancel: cancel, output: out, callbacks: map[string]chan request{}, operations: map[string]context.CancelFunc{}}
	defer core.close()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), MaxFrame)
	lines := make(chan []byte)
	scanDone := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		scanDone <- scanner.Err()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-scanDone:
			return err
		case line := <-lines:
			var message request
			if json.Unmarshal(line, &message) != nil {
				core.send(map[string]any{"error": RPCError{"INVALID_REQUEST", "invalid JSON request"}})
				continue
			}
			if message.Method == "" {
				var id string
				if json.Unmarshal(message.ID, &id) == nil && (strings.HasPrefix(id, "secret-") || strings.HasPrefix(id, "admit-") || strings.HasPrefix(id, "invoke-")) {
					core.mu.Lock()
					ch := core.callbacks[id]
					core.mu.Unlock()
					if ch != nil {
						select {
						case ch <- message:
						default:
							core.cancel()
						}
					}
				}
				continue
			}
			if len(message.ID) == 0 {
				continue
			}
			operationCtx, stop := context.WithCancel(ctx)
			id := string(message.ID)
			core.mu.Lock()
			if _, exists := core.operations[id]; exists {
				core.mu.Unlock()
				stop()
				core.reply(message.ID, nil, errors.New("duplicate active request id"))
				continue
			}
			core.operations[id] = stop
			core.mu.Unlock()
			core.workers.Add(1)
			go func() {
				defer core.workers.Done()
				defer stop()
				defer func() { core.mu.Lock(); delete(core.operations, id); core.mu.Unlock() }()
				result, err := core.call(operationCtx, message.Method, message.Params)
				core.reply(message.ID, result, err)
				if message.Method == "shutdown" && err == nil {
					go core.drain()
				}
			}()
		}
	}
}

func (c *Core) send(value any) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := json.NewEncoder(c.output).Encode(value); err != nil {
		c.cancel()
	}
}
func (c *Core) reply(id json.RawMessage, result any, err error) {
	if err != nil {
		c.send(map[string]any{"id": id, "error": RPCError{"MODEL_CORE_REQUEST_FAILED", safeError(err)}})
		return
	}
	c.send(map[string]any{"id": id, "result": result})
}
func safeError(err error) string {
	// Upstream bodies and credentials belong in neither control errors nor logs.
	if errors.Is(err, context.Canceled) {
		return "operation cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation timed out"
	}
	text := err.Error()
	for _, mark := range []string{"sk-", "Bearer ", "refresh_token", "access_token", "api_key", "https://"} {
		if strings.Contains(text, mark) {
			return "model core operation failed"
		}
	}
	if len(text) > 320 {
		return "model core operation failed"
	}
	return text
}
func (c *Core) vault(ctx context.Context, method, ref string, payload json.RawMessage) (json.RawMessage, error) {
	id := fmt.Sprintf("secret-%d", c.seq.Add(1))
	ch := make(chan request, 64)
	c.mu.Lock()
	c.callbacks[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.callbacks, id); c.mu.Unlock() }()
	params := map[string]any{"ref": ref}
	if payload != nil {
		params["payload"] = payload
	}
	c.send(map[string]any{"event": "secret", "id": id, "method": method, "params": params})
	select {
	case reply := <-ch:
		if reply.Error != nil {
			return nil, errors.New("credential vault request failed")
		}
		var result struct {
			Payload json.RawMessage `json:"payload"`
			OK      bool            `json:"ok"`
		}
		if err := json.Unmarshal(reply.Result, &result); err != nil {
			return nil, errors.New("invalid credential vault reply")
		}
		if method == "secret.set" && !result.OK {
			return nil, errors.New("credential vault did not acknowledge persistence")
		}
		return result.Payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, context.DeadlineExceeded
	}
}
func (c *Core) Get(ctx context.Context, ref string) (json.RawMessage, error) {
	return c.vault(ctx, "secret.get", ref, nil)
}
func (c *Core) Put(ctx context.Context, ref string, payload json.RawMessage) error {
	_, err := c.vault(ctx, "secret.set", ref, payload)
	return err
}

func (c *Core) initialize(ctx context.Context, raw json.RawMessage) (any, error) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil, errors.New("core owner is already initialized")
	}
	c.started = true
	c.mu.Unlock()
	var options Init
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, err
	}
	if options.Protocol != Protocol || options.OwnerID <= 0 {
		return nil, errors.New("unsupported protocol or owner")
	}
	if !filepath.IsAbs(options.DataDir) || !filepath.IsAbs(options.CacheDir) {
		return nil, errors.New("managed directories must be absolute")
	}
	if err := appdir.ConfigureManaged(options.DataDir, options.CacheDir); err != nil {
		return nil, err
	}
	if err := managedstate.Configure(c); err != nil {
		return nil, err
	}
	if err := claudecode.ConfigureManaged(options.ClaudePath); err != nil {
		return nil, err
	}
	if err := plugin.ConfigureManagedRuntime(options.BunPath, options.Plugins); err != nil {
		return nil, err
	}
	if options.Providers != nil || options.Groups != nil {
		if err := provider.Mirror(options.Providers, options.Groups); err != nil {
			return nil, err
		}
	}
	for name, strategy := range routes.Builtin {
		routes.Register(name, strategy)
	}
	c.owner = options.OwnerID
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	c.token = hex.EncodeToString(random)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	c.baseURL = "http://" + listener.Addr().String()
	c.gateway = gateway.New()
	c.gateway.SetManagedCallbackURL(c.baseURL)
	inner := c.gateway.Handler()
	c.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_magpie/claude-mcp/") {
			inner.ServeHTTP(w, r)
			return
		}
		supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(c.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !inferencePath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		ticket := r.Header.Get("X-Xfriend-Invocation")
		if ticket == "" && r.Method != "GET" {
			http.Error(w, "invocation authorization required", http.StatusForbidden)
			return
		}
		invocationCtx, cancel := context.WithCancel(r.Context())
		defer cancel()
		clone := r.Clone(managedstate.WithInvocationCancel(invocationCtx, ticket, cancel))
		clone.Header = r.Header.Clone()
		clone.Header.Set("Authorization", "Bearer magpie-xfriend")
		response := &admissionResponse{ResponseWriter: w, state: managedstate.AuthorizationOf(clone.Context())}
		inner.ServeHTTP(response, clone)
		response.finish()
	}), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return c.ctx }}
	go func() { _ = c.server.Serve(listener) }()
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	result := map[string]any{"protocol": Protocol, "owner_id": c.owner, "base_url": c.baseURL, "token": c.token}
	c.send(map[string]any{"event": "ready", "protocol": Protocol, "owner_id": c.owner, "base_url": c.baseURL, "token": c.token})
	return result, nil
}
func inferencePath(path string) bool {
	switch path {
	case "/v1/models", "/models", "/v1/chat/completions", "/chat/completions", "/v1/responses", "/responses", "/v1/messages", "/messages", "/v1/embeddings", "/embeddings", "/v1/audio/speech", "/audio/speech", "/v1/audio/transcriptions", "/audio/transcriptions", "/v1/images/generations", "/images/generations", "/v1/images/edits", "/images/edits":
		return true
	}
	return strings.HasPrefix(path, "/v1beta/models/")
}
func (c *Core) close() {
	c.cancel()
	provider.CloseManagedNative()
	if c.gateway != nil {
		c.gateway.CloseManaged()
	}
	if c.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		_ = c.server.Shutdown(ctx)
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	_ = plugin.Shutdown(ctx)
	cancel()
	c.workers.Wait()
}

func decode(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return json.Unmarshal(raw, target)
}
func (c *Core) call(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method == "init" {
		return c.initialize(ctx, raw)
	}
	c.mu.Lock()
	ready, closing := c.ready, c.closing
	c.mu.Unlock()
	if !ready || closing {
		return nil, errors.New("core is not initialized")
	}
	var params map[string]json.RawMessage
	if err := decode(raw, &params); err != nil {
		return nil, err
	}
	if _, present := params["owner_id"]; present {
		return nil, errors.New("core owner cannot be changed")
	}
	if method == "cancel" {
		var p struct {
			ID json.RawMessage `json:"id"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		c.mu.Lock()
		stop := c.operations[string(p.ID)]
		c.mu.Unlock()
		if stop != nil {
			stop()
		}
		return map[string]bool{"ok": true}, nil
	}
	if strings.HasPrefix(method, "transfer.") {
		if method != "transfer.open" && method != "transfer.validate" && method != "transfer.seal" {
			return nil, errors.New("external source discovery is not allowed in managed mode")
		}
		return modeltransfer.Call(strings.TrimPrefix(method, "transfer."), raw, "xfriend-model-core-v1")
	}
	if method == "provider.save" || method == "provider.delete" || method == "provider.bind" || method == "group.save" || method == "group.delete" {
		c.mutationMu.Lock()
		defer c.mutationMu.Unlock()
	}
	switch method {
	case "shutdown":
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		return map[string]bool{"ok": true}, nil
	case "barrier":
		return map[string]bool{"ok": true}, nil
	case "import.parse":
		return parseImport(raw)
	case "sources":
		sources, err := managedPlugins(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"presets": provider.Presets(), "plugins": sources, "protocols": []string{"chat", "responses", "anthropic", "gemini"}, "strategies": []string{"smart", "order", "rotate", "usage", "pace", "weight", "free", "fast", "cheap"}, "version": 1}, nil
	case "providers.list":
		out := []map[string]any{}
		for _, p := range provider.All() {
			out = append(out, publicProvider(p))
		}
		return out, nil
	case "provider.save":
		var p struct {
			Provider      provider.Provider `json:"provider"`
			CredentialRef string            `json:"credential_ref"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if p.CredentialRef != "" {
			secret, err := c.Get(ctx, p.CredentialRef)
			if err != nil {
				return nil, err
			}
			var credentials struct {
				APIKey string `json:"api_key"`
				Key    string `json:"key"`
			}
			if err = json.Unmarshal(secret, &credentials); err != nil {
				return nil, err
			}
			p.Provider.Key = credentials.APIKey
			if p.Provider.Key == "" {
				p.Provider.Key = credentials.Key
			}
		}
		if p.Provider.Preset == "xfriend-native" {
			p.Provider.Chat = "http://xfriend-native.invalid/v1"
			p.Provider.Key = "xfriend-managed-bridge"
		}
		if err := provider.Save(p.Provider); err != nil {
			return nil, err
		}
		found, err := provider.Find(p.Provider.ID)
		if err != nil {
			return nil, err
		}
		return publicProvider(*found), nil
	case "provider.delete":
		var p struct {
			ID string `json:"id"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if err := provider.Delete(p.ID); err != nil {
			return nil, err
		}
		if err := provider.DeleteManagedBinding(p.ID); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "provider.bind":
		var p provider.ManagedBinding
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if err := provider.BindManaged(p); err != nil {
			return nil, err
		}
		found, err := provider.Find(p.ID)
		if err != nil {
			return nil, err
		}
		return publicProvider(*found), nil
	case "provider.models":
		var p struct {
			ID      string `json:"id"`
			Refresh bool   `json:"refresh"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		found, err := provider.Find(p.ID)
		if err != nil {
			return nil, err
		}
		if p.Refresh {
			return found.Fetch(ctx)
		}
		return found.Available(), nil
	case "provider.quota":
		var p struct {
			ID string `json:"id"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		found, err := provider.Find(p.ID)
		if err != nil {
			return nil, err
		}
		if found.Account != nil {
			return provider.ManagedPluginUsage(ctx, p.ID)
		}
		all := provider.KeyBalances(ctx)
		out := []provider.SubscriptionQuota{}
		for _, card := range all {
			if card.Provider == p.ID {
				out = append(out, card)
			}
		}
		return out, nil
	case "groups.list":
		return provider.Groups(), nil
	case "group.save":
		var p struct {
			Group provider.Group `json:"group"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if err := provider.SaveGroup(p.Group); err != nil {
			return nil, err
		}
		return p.Group, nil
	case "group.delete":
		var p struct {
			ID string `json:"id"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		if err := provider.DeleteGroup(p.ID); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "trace":
		return publicTrace(c.gateway.Trace(ctx, 0, 0)), nil
	case "plugin.list":
		return managedPlugins(ctx)
	case "auth.methods":
		var p struct {
			SourceID string `json:"source_id"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		plugins, err := managedPlugins(ctx)
		if err != nil {
			return nil, err
		}
		for _, source := range plugins {
			if source.ID == p.SourceID {
				out := make([]map[string]any, 0, len(source.Methods))
				for index, m := range source.Methods {
					out = append(out, map[string]any{"index": index, "type": m.Type, "label": m.Label, "placeholder": m.Placeholder, "keyless": m.Keyless, "disabled": !plugin.ManagedMethodAllowed(m)})
				}
				return out, nil
			}
		}
		return nil, errors.New("plugin source not installed")
	case "auth.start", "auth.finish", "auth.key", "auth.cancel", "auth.prompt", "auth.validate":
		return authCall(ctx, method, raw)
	default:
		return nil, errors.New("unknown managed core method")
	}
}

func managedPlugins(ctx context.Context) ([]plugin.Provider, error) {
	if len(plugin.Load().Plugins) == 0 {
		return provider.ManagedNativeSources(), nil
	}
	sources, err := plugin.Providers(ctx)
	if err != nil {
		return nil, err
	}
	if sources == nil {
		return []plugin.Provider{}, nil
	}
	for i := range sources {
		sources[i].Methods = append([]plugin.Method(nil), sources[i].Methods...)
		for j := range sources[i].Methods {
			sources[i].Methods[j].Disabled = !plugin.ManagedMethodAllowed(sources[i].Methods[j])
		}
	}
	return append(sources, provider.ManagedNativeSources()...), nil
}

func publicProvider(p provider.Provider) map[string]any {
	return map[string]any{"id": p.ID, "name": p.Name, "preset": p.Preset, "chat": p.Chat, "responses": p.Responses, "anthropic": p.Anthropic, "gemini": p.Gemini, "models": p.Models, "routing": p.Routing, "affinity": p.Affinity, "credential_present": p.Key != "" || len(p.Keys) > 0 || p.Account != nil || p.Vertex != nil && p.Vertex.CredentialsJSON != "", "key_count": len(p.Keys) + boolInt(p.Key != ""), "enabled": !p.Off}
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func publicTrace(state gateway.TraceState) map[string]any {
	rows := make([]map[string]any, 0, len(state.Routes))
	for _, r := range state.Routes {
		tries := make([]map[string]any, 0, len(r.Tries))
		for _, attempt := range r.Tries {
			tries = append(tries, map[string]any{"id": attempt.ID, "model": attempt.Model, "done": attempt.Done, "status": attempt.Status, "ms": attempt.Millis, "ttft": attempt.TTFT, "effort": attempt.Effort, "served": attempt.Served, "gone": attempt.Gone})
		}
		var rule *gateway.RuleHit
		if r.Rule != nil {
			copy := *r.Rule
			copy.Folder = ""
			if copy.Classified != nil {
				classified := *copy.Classified
				classified.Error = ""
				copy.Classified = &classified
			}
			rule = &copy
		}
		rows = append(rows, map[string]any{"id": r.ID, "time": r.Time, "model": r.Model, "provider": r.Provider, "group": r.Group, "rule": rule, "affinity": r.Affinity, "order": r.Order, "tries": tries, "done": r.Done, "status": r.Status, "ms": r.Millis, "tokens": r.Tokens, "out": r.Output})
	}
	return map[string]any{"seq": state.Seq, "routes": rows}
}
func authCall(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var p struct {
		SourceID  string            `json:"source_id"`
		Method    int               `json:"method"`
		Inputs    map[string]string `json:"inputs"`
		AccountID string            `json:"account_id"`
		Session   string            `json:"session"`
		Code      string            `json:"code"`
		Key       string            `json:"key"`
		Value     string            `json:"value"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.SourceID == "antigravity" || p.SourceID == "claude" {
		switch method {
		case "auth.start":
			return provider.ManagedNativeStart(ctx, p.SourceID)
		case "auth.key":
			return provider.ManagedNativeKey(p.SourceID, p.Key)
		case "auth.prompt":
			return nil, nil
		case "auth.validate":
			return map[string]string{"error": ""}, nil
		}
	}
	if strings.HasPrefix(p.Session, "native-") {
		switch method {
		case "auth.finish":
			return provider.ManagedNativeFinish(ctx, p.Session, p.Code)
		case "auth.cancel":
			provider.ManagedNativeCancel(p.Session)
			return map[string]bool{"ok": true}, nil
		}
	}
	if method == "auth.start" || method == "auth.key" || method == "auth.prompt" || method == "auth.validate" {
		sources, err := plugin.Providers(ctx)
		if err != nil {
			return nil, err
		}
		allowed := false
		for _, source := range sources {
			if source.ID == p.SourceID && p.Method >= 0 && p.Method < len(source.Methods) {
				allowed = plugin.ManagedMethodAllowed(source.Methods[p.Method])
			}
		}
		if !allowed {
			return nil, errors.New("local CLI sign-in is unavailable in managed mode; use browser or API key")
		}
	}
	switch method {
	case "auth.start":
		return plugin.Authorize(ctx, p.SourceID, p.Method, p.Inputs, p.AccountID)
	case "auth.finish":
		return plugin.Finish(ctx, p.Session, p.Code)
	case "auth.key":
		return plugin.APIKey(ctx, p.SourceID, p.Method, p.Inputs, p.Key, p.AccountID)
	case "auth.cancel":
		if err := plugin.CancelAuthorization(ctx, p.Session); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "auth.prompt":
		return plugin.NextPrompt(ctx, p.SourceID, p.Method, p.Inputs)
	case "auth.validate":
		value, err := plugin.Validate(ctx, p.SourceID, p.Method, p.Key, p.Value)
		return map[string]string{"error": value}, err
	}
	return nil, errors.New("unsupported authentication operation")
}

// DecodeTransfer is exported for the host's file inspector without starting a
// gateway or discovering this computer's external client accounts.
func DecodeTransfer(data, password string) (any, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	return modeltransfer.Open(raw, password)
}

func (c *Core) drain() {
	provider.CloseManagedNative()
	if c.gateway != nil {
		c.gateway.CloseManaged()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if c.server != nil {
		_ = c.server.Shutdown(ctx)
	}
	_ = plugin.Shutdown(ctx)
	c.cancel()
}
