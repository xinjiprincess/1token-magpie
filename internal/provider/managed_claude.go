package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/proc"
)

type managedClaudeLogin struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	token    chan string
	failed   chan error
	finished chan struct{}
	work     string
}

var managedSetupToken = regexp.MustCompile(`sk-ant-oat[0-9A-Za-z_-]+`)

func startManagedClaude(ctx context.Context) (plugin.Authorization, error) {
	binary, _ := claudecode.ManagedPath()
	if binary == "" {
		return plugin.Authorization{}, errors.New("bundled official Claude Code is unavailable")
	}
	root := filepath.Join(appdir.Config(), "native-claude-signin")
	if err := os.MkdirAll(root, 0700); err != nil {
		return plugin.Authorization{}, err
	}
	work, err := os.MkdirTemp(root, "login-")
	if err != nil {
		return plugin.Authorization{}, err
	}
	own, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	cmd := proc.CommandContext(own, binary, "setup-token")
	cmd.Dir = work
	opener, _ := os.Executable()
	opened := filepath.Join(work, "opened-url")
	cmd.Env = append(plugin.ManagedEnvironment(os.Environ()), "CLAUDE_CONFIG_DIR="+work, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+work, "BROWSER="+opener, openedURLEnv+"="+opened, "DISABLE_AUTOUPDATER=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return plugin.Authorization{}, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return plugin.Authorization{}, err
	}
	cmd.Stderr = cmd.Stdout
	if err = cmd.Start(); err != nil {
		cancel()
		return plugin.Authorization{}, err
	}
	login := &managedClaudeLogin{cmd: cmd, input: input, token: make(chan string, 1), failed: make(chan error, 1), finished: make(chan struct{}), work: work}
	flow := &nativeOAuth{source: "claude", ctx: own, cancel: cancel, claude: login}
	links := make(chan string, 1)
	go func() {
		defer close(login.finished)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := ansi.ReplaceAllString(scanner.Text(), "")
			if token := managedSetupToken.FindString(line); token != "" {
				select {
				case login.token <- token:
				default:
				}
			}
			if link := cursorLoginURL.FindString(line); link != "" {
				select {
				case links <- link:
				default:
				}
			}
		}
		err := cmd.Wait()
		if err != nil {
			login.failed <- errors.New("official Claude Code authorization failed")
		} else {
			login.failed <- nil
		}
	}()
	session := "native-" + randomToken(12)
	nativeFlows.Lock()
	nativeFlows.flows[session] = flow
	nativeFlows.Unlock()
	go func() { <-own.Done(); _ = input.Close(); <-login.finished; _ = os.RemoveAll(work) }()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case link := <-links:
			return plugin.Authorization{Session: session, URL: link, Method: "code", Instructions: "使用官方 Claude Code 完成授权；如页面显示授权码，请粘贴回来。"}, nil
		case <-tick.C:
			if raw, err := os.ReadFile(opened); err == nil && strings.HasPrefix(string(raw), "https://") {
				return plugin.Authorization{Session: session, URL: string(raw), Method: "code", Instructions: "使用官方 Claude Code 完成授权。"}, nil
			}
		case <-ctx.Done():
			ManagedNativeCancel(session)
			return plugin.Authorization{}, ctx.Err()
		case <-deadline.C:
			ManagedNativeCancel(session)
			return plugin.Authorization{}, errors.New("official Claude Code did not provide an authorization URL")
		case err := <-login.failed:
			if err == nil && len(login.token) == 0 {
				err = errors.New("official Claude Code produced no setup-token")
			}
			if err != nil {
				ManagedNativeCancel(session)
				return plugin.Authorization{}, err
			}
			return plugin.Authorization{Session: session, Method: "auto"}, nil
		}
	}
}
func finishManagedClaude(ctx context.Context, session string, flow *nativeOAuth, code string) (plugin.Saved, error) {
	login := flow.claude
	if code != "" {
		if _, err := io.WriteString(login.input, code+"\n"); err != nil {
			return plugin.Saved{}, errors.New("official Claude Code no longer accepts an authorization code")
		}
	}
	var token string
	select {
	case token = <-login.token:
	case <-ctx.Done():
		return plugin.Saved{}, ctx.Err()
	case <-flow.ctx.Done():
		return plugin.Saved{}, flow.ctx.Err()
	}
	token, err := checkClaudeSetupToken(token)
	if err != nil {
		return plugin.Saved{}, err
	}
	account := managedNativeAccount{Source: "claude", User: claudeTokenName(token), SetupToken: token}
	id := nativeKey("claude", account.User)
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.closed || flow.ctx.Err() != nil {
		return plugin.Saved{}, errors.New("native authorization cancelled")
	}
	if err = saveNativeAccount(id, account); err != nil {
		return plugin.Saved{}, err
	}
	flow.closed = true
	flow.cancel()
	nativeFlows.Lock()
	delete(nativeFlows.flows, session)
	nativeFlows.Unlock()
	return plugin.Saved{Provider: "claude", Account: id}, nil
}
func managedClaudeDir(id string) string {
	return filepath.Join(appdir.Config(), "native-claude", strings.ReplaceAll(id, "#", "-"))
}
func managedClaudeProvider(binding ManagedBinding, account managedNativeAccount) Provider {
	acct := &Account{Agent: "claude", User: binding.AccountID, Stream: true, Plan: "setup-token"}
	p := claudeProvider(acct)
	p.ID, p.Preset = binding.ID, "claude"
	acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
	acct.fetch = func(context.Context) ([]catalog.Model, error) { return acct.models(), nil }
	acct.token = func(ctx context.Context) (string, error) {
		dir := managedClaudeDir(binding.AccountID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
		profile := filepath.Join(dir, ".claude.json")
		if _, err := os.Stat(profile); os.IsNotExist(err) {
			raw, _ := json.Marshal(map[string]bool{"hasCompletedOnboarding": true})
			if err = os.WriteFile(profile, raw, 0600); err != nil {
				return "", err
			}
		}
		return dir, nil
	}
	if binding.Name != "" {
		p.Name = binding.Name
	}
	return p
}
func managedClaudeDirToken(dir string) string {
	for id, account := range nativeAccounts() {
		if account.Source == "claude" && filepath.Clean(dir) == managedClaudeDir(id) {
			return account.SetupToken
		}
	}
	return ""
}
