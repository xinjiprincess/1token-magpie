package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/managedstate"
	"github.com/yetone/magpie/internal/plugin"
)

type managedNativeAccount struct {
	Source     string     `json:"source"`
	User       string     `json:"user"`
	Plan       string     `json:"plan,omitempty"`
	Google     googleAuth `json:"google,omitempty"`
	SetupToken string     `json:"setup_token,omitempty"`
}

var nativeMu sync.Mutex
var nativeRefresh sync.Map

func nativeLock(key string) *sync.Mutex {
	value, _ := nativeRefresh.LoadOrStore(key, new(sync.Mutex))
	return value.(*sync.Mutex)
}
func nativeAccounts() map[string]managedNativeAccount {
	raw, _ := managedstate.Read("native-auth")
	accounts := map[string]managedNativeAccount{}
	_ = json.Unmarshal(raw, &accounts)
	return accounts
}
func saveNativeAccount(key string, account managedNativeAccount) error {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	raw, err := managedstate.Read("native-auth")
	if err != nil {
		return err
	}
	accounts := map[string]managedNativeAccount{}
	if err = json.Unmarshal(raw, &accounts); err != nil {
		return err
	}
	accounts[key] = account
	raw, err = json.Marshal(accounts)
	if err != nil {
		return err
	}
	return managedstate.Write("native-auth", raw)
}
func nativeKey(source, user string) string {
	sum := sha256.Sum256([]byte(user))
	return source + "#" + hex.EncodeToString(sum[:8])
}
func ManagedNativeSources() []plugin.Provider {
	accounts := nativeAccounts()
	out := []plugin.Provider{}
	for _, id := range []string{"antigravity", "claude"} {
		source := plugin.Provider{ID: id, Name: map[string]string{"antigravity": "Antigravity", "claude": "Claude 订阅（官方 Claude Code）"}[id], Methods: []plugin.Method{{Type: "oauth", Label: "浏览器授权"}, {Type: "api", Label: "导入 OAuth JSON"}}, Accounts: []plugin.Account{}, Models: []plugin.Model{}}
		if id == "claude" {
			source.Methods[0].Label = "官方 CLI 长效授权"
			source.Methods[1].Label = "长效 setup-token"
		}
		for key, account := range accounts {
			if account.Source == id {
				source.Accounts = append(source.Accounts, plugin.Account{Key: key, Type: "oauth", AccountID: account.User})
				source.SignedIn = true
			}
		}
		var models []catalog.Model
		if app, ok := googleAppOf(id); ok {
			models = (googleAccount{app: app}).fallbackModels()
		} else {
			models = catalog.Provider("anthropic")
		}
		for _, model := range models {
			source.Models = append(source.Models, plugin.Model{ID: model.ID, Name: model.Name, Context: model.Context})
		}
		out = append(out, source)
	}
	return out
}
func managedNativeProvider(binding ManagedBinding) (Provider, bool) {
	account, ok := nativeAccounts()[binding.AccountID]
	if !ok || account.Source != binding.SourceID {
		return Provider{}, false
	}
	if app, ok := googleAppOf(account.Source); ok {
		g := googleAccount{app: app, user: account.User, auth: account.Google, managed: binding.AccountID}
		p := googleProvider(g, account.Plan)
		p.ID, p.Preset = binding.ID, binding.SourceID
		if binding.Name != "" {
			p.Name = binding.Name
		}
		p.Account.fetch = func(ctx context.Context) ([]catalog.Model, error) { return g.models(ctx) }
		return p, true
	}
	if account.Source == "claude" {
		return managedClaudeProvider(binding, account), true
	}
	return Provider{}, false
}
func managedNativeQuota(ctx context.Context, binding ManagedBinding) (any, error) {
	account, ok := nativeAccounts()[binding.AccountID]
	if !ok {
		return nil, errors.New("native account not found")
	}
	if app, ok := googleAppOf(account.Source); ok {
		g := googleAccount{app: app, user: account.User, auth: account.Google, managed: binding.AccountID}
		return g.quota(ctx, account.Plan), nil
	}
	return map[string]any{"state": "unknown", "reason": "setup_token_inference_only"}, nil
}

type nativeOAuth struct {
	mu                                sync.Mutex
	source, verifier, state, redirect string
	code                              chan string
	server                            *http.Server
	cancel                            context.CancelFunc
	ctx                               context.Context
	closed                            bool
	claude                            *managedClaudeLogin
}

var nativeFlows = struct {
	sync.Mutex
	flows map[string]*nativeOAuth
}{flows: map[string]*nativeOAuth{}}

func ManagedNativeStart(ctx context.Context, source string) (plugin.Authorization, error) {
	if source == "claude" {
		return startManagedClaude(ctx)
	}
	app, ok := googleAppOf(source)
	if !ok || source != "antigravity" {
		return plugin.Authorization{}, errors.New("unsupported native authorization")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return plugin.Authorization{}, err
	}
	own, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	flow := &nativeOAuth{source: source, verifier: randomToken(48), state: randomToken(24), code: make(chan string, 1), ctx: own, cancel: cancel}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	flow.redirect = "http://" + net.JoinHostPort(app.loopback, port) + app.callback
	flow.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != app.callback || r.URL.Query().Get("state") != flow.state || r.URL.Query().Get("code") == "" {
			http.Error(w, "invalid callback", 400)
			return
		}
		select {
		case flow.code <- r.URL.Query().Get("code"):
		default:
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "授权已接收，请返回 Xfriend。")
	})}
	go func() { _ = flow.server.Serve(listener) }()
	session := "native-" + randomToken(12)
	nativeFlows.Lock()
	nativeFlows.flows[session] = flow
	nativeFlows.Unlock()
	go func() { <-own.Done(); _ = flow.server.Close() }()
	hash := sha256.Sum256([]byte(flow.verifier))
	query := url.Values{"client_id": {app.clientID}, "redirect_uri": {flow.redirect}, "response_type": {"code"}, "scope": {strings.Join(app.scopes, " ")}, "state": {flow.state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "access_type": {"offline"}, "prompt": {"consent"}}
	return plugin.Authorization{Session: session, URL: googleAuthorizeURL + "?" + query.Encode(), Method: "auto", Instructions: "在浏览器完成 Google 授权；远程访问可粘贴完整回调地址。"}, nil
}
func ManagedNativeFinish(ctx context.Context, session, code string) (plugin.Saved, error) {
	nativeFlows.Lock()
	flow := nativeFlows.flows[session]
	nativeFlows.Unlock()
	if flow == nil {
		return plugin.Saved{}, errors.New("native authorization session not found")
	}
	if flow.claude != nil {
		return finishManagedClaude(ctx, session, flow, code)
	}
	if strings.HasPrefix(code, "http://") || strings.HasPrefix(code, "https://") {
		callback, err := url.Parse(code)
		if err != nil || callback.Query().Get("state") != flow.state {
			return plugin.Saved{}, errors.New("invalid native authorization state")
		}
		code = callback.Query().Get("code")
	}
	if code == "" {
		select {
		case code = <-flow.code:
		case <-flow.ctx.Done():
			return plugin.Saved{}, flow.ctx.Err()
		case <-ctx.Done():
			return plugin.Saved{}, ctx.Err()
		}
	}
	app, _ := googleAppOf(flow.source)
	exchange, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(flow.ctx, cancel)
	defer stop()
	google, plan, err := googleExchange(exchange, app, code, flow.verifier, flow.redirect)
	if err != nil {
		return plugin.Saved{}, err
	}
	account := managedNativeAccount{Source: flow.source, User: google.user, Plan: plan, Google: google.auth}
	key := nativeKey(flow.source, google.user)
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.closed || flow.ctx.Err() != nil {
		return plugin.Saved{}, errors.New("native authorization cancelled")
	}
	if err = saveNativeAccount(key, account); err != nil {
		return plugin.Saved{}, err
	}
	flow.closed = true
	flow.cancel()
	nativeFlows.Lock()
	delete(nativeFlows.flows, session)
	nativeFlows.Unlock()
	return plugin.Saved{Provider: flow.source, Account: key}, nil
}
func ManagedNativeCancel(session string) bool {
	nativeFlows.Lock()
	flow := nativeFlows.flows[session]
	delete(nativeFlows.flows, session)
	nativeFlows.Unlock()
	if flow == nil {
		return false
	}
	flow.mu.Lock()
	flow.closed = true
	flow.cancel()
	flow.mu.Unlock()
	return true
}
func ManagedNativeKey(source, key string) (plugin.Saved, error) {
	account := managedNativeAccount{Source: source}
	if source == "claude" {
		token, err := checkClaudeSetupToken(key)
		if err != nil {
			return plugin.Saved{}, err
		}
		account.SetupToken = token
		account.User = claudeTokenName(token)
	} else if source == "antigravity" {
		if json.Unmarshal([]byte(key), &account.Google) != nil || account.Google.RefreshToken == "" {
			return plugin.Saved{}, errors.New("invalid native OAuth JSON")
		}
		account.User = claudeTokenVersion(account.Google.RefreshToken)
	} else {
		return plugin.Saved{}, errors.New("unsupported native source")
	}
	id := nativeKey(source, account.User)
	if err := saveNativeAccount(id, account); err != nil {
		return plugin.Saved{}, err
	}
	return plugin.Saved{Provider: source, Account: id}, nil
}

// ManagedGoogleForTest redirects only this process's Google transport fixtures.
func ManagedGoogleForTest(authorize, token, user, base string) func() {
	oldA, oldT, oldU, oldP, oldD := googleAuthorizeURL, googleTokenURL, googleUserInfoURL, codeAssistProd, codeAssistDaily
	googleAuthorizeURL, googleTokenURL, googleUserInfoURL, codeAssistProd, codeAssistDaily = authorize, token, user, base, base
	return func() {
		googleAuthorizeURL, googleTokenURL, googleUserInfoURL, codeAssistProd, codeAssistDaily = oldA, oldT, oldU, oldP, oldD
	}
}

// CloseManagedNative cancels authorization processes owned by this core.
func CloseManagedNative() {
	nativeFlows.Lock()
	sessions := make([]string, 0, len(nativeFlows.flows))
	for id := range nativeFlows.flows {
		sessions = append(sessions, id)
	}
	nativeFlows.Unlock()
	for _, id := range sessions {
		ManagedNativeCancel(id)
	}
}
