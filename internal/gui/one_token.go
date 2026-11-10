package gui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The native 1token account: browser authorization with PKCE, a relay key,
// the models that key may use, and the wallet. No provider plugin is needed.

const (
	oneTokenClientID     = "magpie"
	oneTokenCallbackPath = "/oauth/callback"
	oneTokenFileName     = "one-token.json"
	oneTokenProviderID   = "1token"
)

var (
	oneTokenMu        sync.Mutex
	oneTokenFlow      *oneTokenPending
	oneTokenLastError string
	oneTokenClient    = &http.Client{Timeout: 30 * time.Second}
	oneTokenPluginOne sync.Once
)

var errOneTokenUnauthorized = errors.New("1token key is no longer accepted")

type oneTokenModel struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Context int     `json:"context,omitempty"`
	Input   float64 `json:"input,omitempty"`
	Output  float64 `json:"output,omitempty"`
}

type oneTokenState struct {
	Site       string          `json:"site"`
	Username   string          `json:"username,omitempty"`
	TokenName  string          `json:"tokenName,omitempty"`
	Key        string          `json:"key,omitempty"`
	LoggedInAt time.Time       `json:"loggedInAt,omitempty"`
	Models     []oneTokenModel `json:"models,omitempty"`
	Balance    string          `json:"balance,omitempty"`
}

type oneTokenPending struct {
	state     string
	verifier  string
	redirect  string
	link      string
	server    *http.Server
	listener  net.Listener
	timer     *time.Timer
	closeOnce sync.Once
}

func (p *oneTokenPending) close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if p.timer != nil {
			p.timer.Stop()
		}
		if p.server != nil {
			_ = p.server.Close()
		}
		if p.listener != nil {
			_ = p.listener.Close()
		}
	})
}

// linger keeps the loopback listener up briefly after an answer: a second
// tab may still return to the same redirect, and a listener closed as the
// answer is written is the browser's 127.0.0.1 connection refused.
func (p *oneTokenPending) linger() {
	if p == nil {
		return
	}
	if p.timer != nil {
		p.timer.Reset(30 * time.Second)
	} else {
		time.AfterFunc(30*time.Second, p.close)
	}
}

func oneTokenSite() string {
	s := strings.TrimRight(strings.TrimSpace(os.Getenv("ONETOKEN_SITE")), "/")
	if s == "" {
		s = "https://www.ciyuancenter.cc"
	}
	return s
}

func oneTokenPath() string {
	return filepath.Join(settings.Dir(), oneTokenFileName)
}

func loadOneTokenState() oneTokenState {
	var s oneTokenState
	b, err := os.ReadFile(oneTokenPath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func saveOneTokenState(s oneTokenState) error {
	if s.Site == "" {
		s.Site = oneTokenSite()
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := settings.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := oneTokenPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func oneTokenRetireOldPlugin() {
	oneTokenPluginOne.Do(func() {
		for _, p := range plugin.Load().Plugins {
			if plugin.Name(p.Spec) == "1_token_magpie" || strings.HasSuffix(strings.ToLower(p.Spec), "1_token_magpie") {
				_ = provider.SetPluginOff(context.Background(), p.Spec, true)
			}
		}
	})
}

func oneTokenSyncProvider(s oneTokenState) error {
	ids := make([]string, 0, len(s.Models))
	for _, m := range s.Models {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	sort.Strings(ids)
	p := provider.Provider{
		ID:        oneTokenProviderID,
		Name:      "1token",
		Icon:      "one-token",
		Preset:    oneTokenProviderID,
		Chat:      oneTokenSite() + "/v1",
		ModelsURL: oneTokenSite() + "/v1/models",
		Key:       s.Key,
		Models:    ids,
		NoModels:  len(ids) == 0,
	}
	if old, err := provider.Find(oneTokenProviderID); err == nil && old != nil {
		p = *old
		p.Name = "1token"
		p.Icon = "one-token"
		p.Preset = oneTokenProviderID
		p.Chat = oneTokenSite() + "/v1"
		p.ModelsURL = oneTokenSite() + "/v1/models"
		p.Key = s.Key
		p.Models = ids
		p.NoModels = len(ids) == 0
	}
	return provider.Save(p)
}

func oneTokenDeleteProvider() {
	_ = provider.Delete(oneTokenProviderID)
}

func oneTokenOpen(w Windows, link string) error {
	// The app's WebView2 multi-window path is not stable in this Wails beta;
	// the gate is in-app and 1token's own authorization page opens in the
	// system browser, with the loopback callback returning to the app.
	return w.OpenURL(link)
}

func oneTokenStart(w Windows) (map[string]any, error) {
	oneTokenRetireOldPlugin()
	// A second click while the browser is still open must not close the
	// callback listener the first page is about to return to: reuse the
	// pending authorization and open its link again.
	oneTokenMu.Lock()
	pending := oneTokenFlow
	oneTokenMu.Unlock()
	if pending != nil && pending.link != "" {
		if err := oneTokenOpen(w, pending.link); err != nil {
			return nil, err
		}
		return map[string]any{"started": true, "url": pending.link, "reused": true}, nil
	}
	verifier, challenge, state, err := oneTokenPKCE()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, oneTokenCallbackPath)
	flow := &oneTokenPending{state: state, verifier: verifier, redirect: redirect, listener: ln}
	mux := http.NewServeMux()
	mux.HandleFunc(oneTokenCallbackPath, func(rw http.ResponseWriter, r *http.Request) {
		oneTokenHandleCallback(rw, r, flow)
	})
	flow.server = &http.Server{Handler: mux}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", oneTokenClientID)
	q.Set("redirect_uri", redirect)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if host, err := os.Hostname(); err == nil && host != "" {
		q.Set("device", host)
	}
	flow.link = oneTokenSite() + "/oauth/authorize?" + q.Encode()

	oneTokenMu.Lock()
	old := oneTokenFlow
	oneTokenFlow = flow
	oneTokenLastError = ""
	oneTokenMu.Unlock()
	old.close()

	go func() { _ = flow.server.Serve(ln) }()
	flow.timer = time.AfterFunc(5*time.Minute, func() {
		oneTokenMu.Lock()
		if oneTokenFlow == flow {
			oneTokenFlow = nil
			oneTokenLastError = "登录授权超时，请重新登录"
		}
		oneTokenMu.Unlock()
		flow.close()
		closeOneTokenWindow()
	})

	if err := oneTokenOpen(w, flow.link); err != nil {
		flow.close()
		oneTokenMu.Lock()
		if oneTokenFlow == flow {
			oneTokenFlow = nil
		}
		oneTokenMu.Unlock()
		return nil, err
	}
	return map[string]any{"started": true, "url": flow.link}, nil
}

func oneTokenPKCE() (verifier, challenge, state string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	buf = make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return
	}
	state = base64.RawURLEncoding.EncodeToString(buf)
	return
}

func oneTokenStateOnly() {
	oneTokenMu.Lock()
	oneTokenFlow = nil
	oneTokenMu.Unlock()
}

func oneTokenHandleCallback(rw http.ResponseWriter, r *http.Request, flow *oneTokenPending) {
	oneTokenMu.Lock()
	current := oneTokenFlow == flow
	oneTokenMu.Unlock()
	if !current {
		// A tab from an earlier click, or a second tab after a successful
		// login, came back late. Its listener is kept for a while on
		// purpose; answer it without failing the login that already
		// worked.
		if s := loadOneTokenState(); strings.TrimSpace(s.Key) != "" {
			oneTokenPage(rw, true, "")
		} else {
			oneTokenPage(rw, false, "授权已失效，请回到 1token_magpie 重新登录")
		}
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		oneTokenMu.Lock()
		if oneTokenFlow == flow {
			oneTokenLastError = q.Get("error_description")
			if oneTokenLastError == "" {
				oneTokenLastError = e
			}
		}
		oneTokenMu.Unlock()
		oneTokenStateOnly()
		flow.linger()
		closeOneTokenWindow()
		oneTokenPage(rw, false, oneTokenLastError)
		return
	}
	if q.Get("state") != flow.state || q.Get("code") == "" {
		oneTokenMu.Lock()
		if oneTokenFlow == flow {
			oneTokenLastError = "授权回调校验失败，请重新登录"
		}
		oneTokenMu.Unlock()
		oneTokenStateOnly()
		flow.linger()
		closeOneTokenWindow()
		oneTokenPage(rw, false, "授权回调校验失败，请重新登录")
		return
	}
	key, username, tokenName, err := oneTokenExchange(flow, q.Get("code"))
	if err != nil {
		oneTokenMu.Lock()
		if oneTokenFlow == flow {
			oneTokenLastError = err.Error()
		}
		oneTokenMu.Unlock()
		oneTokenStateOnly()
		flow.linger()
		closeOneTokenWindow()
		oneTokenPage(rw, false, err.Error())
		return
	}
	models, balance, err := oneTokenFetch(key)
	if err != nil {
		oneTokenMu.Lock()
		if oneTokenFlow == flow {
			oneTokenLastError = err.Error()
		}
		oneTokenMu.Unlock()
		oneTokenStateOnly()
		flow.linger()
		closeOneTokenWindow()
		oneTokenPage(rw, false, err.Error())
		return
	}
	s := oneTokenState{Site: oneTokenSite(), Username: username, TokenName: tokenName, Key: key, LoggedInAt: time.Now(), Models: models, Balance: balance}
	if err := saveOneTokenState(s); err != nil {
		oneTokenPage(rw, false, err.Error())
		return
	}
	_ = oneTokenSyncProvider(s)
	oneTokenMu.Lock()
	if oneTokenFlow == flow {
		oneTokenFlow = nil
	}
	oneTokenLastError = ""
	oneTokenMu.Unlock()
	flow.linger()
	closeOneTokenWindow()
	oneTokenPage(rw, true, "")
}

func oneTokenExchange(flow *oneTokenPending, code string) (key, username, tokenName string, err error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", oneTokenClientID)
	form.Set("code", code)
	form.Set("code_verifier", flow.verifier)
	form.Set("redirect_uri", flow.redirect)
	resp, err := oneTokenClient.PostForm(oneTokenSite()+"/api/oauth/token", form)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken      string `json:"access_token"`
		Username         string `json:"username"`
		TokenName        string `json:"token_name"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", "", "", err
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		why := out.ErrorDescription
		if why == "" {
			why = out.Error
		}
		if why == "" {
			why = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", "", "", errors.New("兑换 1token 授权失败：" + why)
	}
	return out.AccessToken, out.Username, out.TokenName, nil
}

func oneTokenGetJSON(path, key string, v any) error {
	req, err := http.NewRequest(http.MethodGet, oneTokenSite()+path, nil)
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oneTokenClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errOneTokenUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

func oneTokenFetch(key string) ([]oneTokenModel, string, error) {
	type entry struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		ServedHere    bool     `json:"servedHere"`
		Published     *bool    `json:"published"`
		PriceVisible  *bool    `json:"priceVisible"`
		BillingUnit   string   `json:"billingUnit"`
		Endpoints     []string `json:"endpoints"`
		ContextWindow int      `json:"contextWindow"`
		InputPrice    float64  `json:"inputPrice"`
		OutputPrice   float64  `json:"outputPrice"`
		Output        []string `json:"output"`
	}
	type catalog struct {
		Data struct {
			Providers []struct {
				Models []entry `json:"models"`
			} `json:"providers"`
		} `json:"data"`
		Providers []struct {
			Models []entry `json:"models"`
		} `json:"providers"`
	}
	var cat catalog
	if err := oneTokenGetJSON("/api/catalog", "", &cat); err != nil {
		return nil, "", err
	}
	providers := cat.Data.Providers
	if len(providers) == 0 {
		providers = cat.Providers
	}
	byID := map[string]oneTokenModel{}
	for _, p := range providers {
		for _, m := range p.Models {
			if m.ID == "" || !m.ServedHere {
				continue
			}
			if m.Published != nil && !*m.Published {
				continue
			}
			if m.PriceVisible != nil && !*m.PriceVisible {
				continue
			}
			if m.BillingUnit != "" && m.BillingUnit != "tokens" {
				continue
			}
			if m.OutputPrice <= 0 {
				continue
			}
			chat := len(m.Endpoints) == 0
			for _, e := range m.Endpoints {
				if e == "chat.completions" {
					chat = true
				}
			}
			if !chat {
				continue
			}
			if len(m.Output) > 0 {
				text := false
				for _, o := range m.Output {
					if o == "text" {
						text = true
					}
				}
				if !text {
					continue
				}
			}
			name := m.Name
			if name == "" {
				name = m.ID
			}
			byID[m.ID] = oneTokenModel{ID: m.ID, Name: name, Context: m.ContextWindow, Input: m.InputPrice, Output: m.OutputPrice}
		}
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	allowed := map[string]bool{}
	if err := oneTokenGetJSON("/v1/models", key, &list); err != nil {
		if errors.Is(err, errOneTokenUnauthorized) {
			return nil, "", err
		}
		// A catalog that answered is enough for an older gateway without
		// a token-scoped list; a 401 above is not.
		if len(byID) == 0 {
			return nil, "", err
		}
	}
	for _, m := range list.Data {
		if m.ID != "" {
			allowed[m.ID] = true
		}
	}
	models := make([]oneTokenModel, 0, len(byID))
	for id, m := range byID {
		if len(allowed) > 0 && !allowed[id] {
			continue
		}
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	balance := ""
	var b struct {
		XToken struct {
			RemainingUSD float64 `json:"remaining_usd"`
			UsedUSD      float64 `json:"used_usd"`
		} `json:"xtoken"`
		Balance    float64 `json:"balance"`
		TotalUsage float64 `json:"total_usage"`
	}
	if err := oneTokenGetJSON("/v1/balance", key, &b); err == nil {
		left := b.XToken.RemainingUSD
		if left == 0 {
			left = b.Balance
		}
		balance = fmt.Sprintf("$%.2f", left)
	} else if errors.Is(err, errOneTokenUnauthorized) {
		return nil, "", err
	}
	return models, balance, nil
}

func oneTokenPage(rw http.ResponseWriter, ok bool, why string) {
	title, body := "授权成功", "授权已完成。请回到 1token_magpie。"
	if !ok {
		title, body = "授权未完成", html.EscapeString(why)
		if body == "" {
			body = "请回到 1token_magpie 重新登录。"
		}
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Frame-Options", "DENY")
	_, _ = io.WriteString(rw, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>`+title+`</title><body style="margin:0;min-height:100vh;display:grid;place-items:center;background:#0b1020;color:#e8ecf8;font:16px/1.7 system-ui,sans-serif;text-align:center"><div><h1 style="font-size:23px;margin:0 0 8px">`+title+`</h1><p style="margin:0;opacity:.8">`+body+`</p></div></body></html>`)
}

func oneTokenStatus() map[string]any {
	oneTokenRetireOldPlugin()
	s := loadOneTokenState()
	if s.Site == "" {
		s.Site = oneTokenSite()
	}
	if strings.TrimSpace(s.Key) != "" {
		if _, err := provider.Find(oneTokenProviderID); err != nil {
			_ = oneTokenSyncProvider(s)
		}
	}
	oneTokenMu.Lock()
	pending := oneTokenFlow != nil
	last := oneTokenLastError
	oneTokenMu.Unlock()
	if strings.TrimSpace(s.Key) != "" {
		last = ""
	}
	models := s.Models
	if models == nil {
		models = []oneTokenModel{}
	}
	return map[string]any{
		"loggedIn":  strings.TrimSpace(s.Key) != "",
		"pending":   pending,
		"site":      s.Site,
		"username":  s.Username,
		"tokenName": s.TokenName,
		"models":    models,
		"balance":   s.Balance,
		"error":     last,
	}
}

func oneTokenPaste(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("令牌为空")
	}
	models, balance, err := oneTokenFetch(key)
	if err != nil {
		return err
	}
	s := loadOneTokenState()
	s.Site = oneTokenSite()
	s.Key = key
	s.Models = models
	s.Balance = balance
	s.LoggedInAt = time.Now()
	if s.Username == "" {
		s.Username = "API key"
	}
	if err := saveOneTokenState(s); err != nil {
		return err
	}
	return oneTokenSyncProvider(s)
}

func oneTokenRefresh() error {
	s := loadOneTokenState()
	if strings.TrimSpace(s.Key) == "" {
		return errors.New("尚未登录 1token")
	}
	models, balance, err := oneTokenFetch(s.Key)
	if err != nil {
		if errors.Is(err, errOneTokenUnauthorized) {
			s.Key = ""
			s.Models = nil
			s.Balance = ""
			_ = saveOneTokenState(s)
			oneTokenDeleteProvider()
		}
		oneTokenMu.Lock()
		oneTokenLastError = err.Error()
		oneTokenMu.Unlock()
		return err
	}
	s.Site = oneTokenSite()
	s.Models = models
	s.Balance = balance
	oneTokenMu.Lock()
	oneTokenLastError = ""
	oneTokenMu.Unlock()
	if err := saveOneTokenState(s); err != nil {
		return err
	}
	return oneTokenSyncProvider(s)
}

func oneTokenLogout() error {
	oneTokenDeleteProvider()
	s := oneTokenState{Site: oneTokenSite()}
	_ = saveOneTokenState(s)
	oneTokenMu.Lock()
	oneTokenLastError = ""
	oneTokenMu.Unlock()
	closeOneTokenWindow()
	return nil
}

func oneTokenRoutes(mux *http.ServeMux, w Windows) {
	local := func(rw http.ResponseWriter, r *http.Request) bool {
		if !loopbackPeer(r.RemoteAddr) {
			http.Error(rw, "only this computer may use the 1token account", http.StatusForbidden)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/one-token/status", func(rw http.ResponseWriter, r *http.Request) {
		if !local(rw, r) {
			return
		}
		writeJSON(rw, oneTokenStatus())
	})
	mux.HandleFunc("POST /api/one-token/start", func(rw http.ResponseWriter, r *http.Request) {
		if !local(rw, r) {
			return
		}
		out, err := oneTokenStart(w)
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, out)
	})
	mux.HandleFunc("POST /api/one-token/register", func(rw http.ResponseWriter, r *http.Request) {
		if !local(rw, r) {
			return
		}
		link := oneTokenSite() + "/register"
		if err := oneTokenOpen(w, link); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, map[string]any{"opened": link})
	})
	mux.HandleFunc("POST /api/one-token/token", func(rw http.ResponseWriter, r *http.Request) {
		if !local(rw, r) {
			return
		}
		var in struct {
			Key string `json:"key"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
		if err := oneTokenPaste(in.Key); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, oneTokenStatus())
	})
	mux.HandleFunc("POST /api/one-token/refresh", func(rw http.ResponseWriter, r *http.Request) {
		if !local(rw, r) {
			return
		}
		if err := oneTokenRefresh(); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, oneTokenStatus())
	})
	mux.HandleFunc("POST /api/one-token/logout", func(rw http.ResponseWriter, r *http.Request) {
		if !local(rw, r) {
			return
		}
		if err := oneTokenLogout(); err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, oneTokenStatus())
	})
	mux.HandleFunc("GET /api/one-token/key", func(rw http.ResponseWriter, r *http.Request) {
		if !local(rw, r) {
			return
		}
		s := loadOneTokenState()
		writeJSON(rw, map[string]any{"key": s.Key})
	})
}
