# 1token_magpie

This is the native **1token** fork of [Magpie](https://github.com/yetone/magpie),
based on Magpie `v0.1.2438` at commit
`80af3b86a449c53bd0d26b08ac4d0a4cac191948`.

The desktop app keeps Magpie's gateway and agent integration, but its own
account flow is a native Go implementation. It no longer depends on a Bun
provider plugin for 1token login, models, balance, or the relay key.

## What is changed

- Application, window, tray, shortcut, and installer branding are
  `1token_magpie`.
- 1token OAuth with PKCE and `client_id=magpie`, opened in the system browser.
- A login gate covers the app until the user signs in, registers, or pastes an
  API key. The gate cannot be dismissed into the main UI.
- After login, the token's chat models and USD API balance are synced
  automatically. Video models stay out of the chat model list.
- The old `1_token_magpie` provider plugin is retired at runtime. Its saved key
  is kept but does not count as a native login.
- Upstream Magpie update checks are disabled so they cannot replace this fork
  with the original Magpie binary.
- The loopback OAuth callback is kept alive briefly after success, and repeated
  login clicks reuse the pending authorization. This avoids the
  `127.0.0.1` connection-refused page after clicking login twice.

## Layout

```text
internal/gui/one_token*.go       native account, OAuth, gate API
internal/gui/assets/plugins/one-token/
                                 gate and account page (CSS/JS)
```

The generated Windows resources and launcher are not committed:
`rsrc_windows_*.syso` and `internal/update/launcher-*.com` are rebuilt by the
Makefile.

## Build requirements

- Go **1.26.8** (see `go.mod`)
- Node.js 22+ only if you use the optional overlay build script
- Windows: no cgo; WebView2 runs through Go's COM bindings
- Linux: cgo with GTK 3 and WebKitGTK 4.1

## Build Windows

On Windows, or cross-compiling from Ubuntu (WebView2 needs no cgo):

```sh
make release-windows
```

Output is under `dist/`, named with the upstream `magpie` prefix:

```text
dist/magpie-windows-amd64.exe
dist/magpie-windows-amd64.com
```

The launcher `.com` must stay beside the `.exe`; it is the console entry point
that `magpie` uses in a terminal. Rename both to `1token_magpie.exe` and
`1token_magpie.com` when building a branded package.

Cross-compiling only needs Go:

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -tags production,comlauncher -trimpath -buildvcs=false \
  -ldflags="-s -w -H windowsgui -X main.version=0.1.2438+1token.20261109" \
  -o dist/magpie-windows-amd64.exe .
```

## Build Linux

Use Ubuntu 24.04 or another distribution with WebKitGTK 4.1:

```sh
sudo apt install -y build-essential pkg-config \
  libgtk-3-dev libwebkit2gtk-4.1-dev \
  libayatana-appindicator3-dev librsvg2-dev

make build          # tags: production,gtk3
./magpie app
```

Wails' Linux GUI needs cgo, so build it natively on Linux. Cross-compiling the
Linux GUI from Windows is not supported by this repository's Makefile.

## Installer

The Windows IExpress installer is built separately on Windows from the
portable output. It needs `1token_magpie.exe`, its `.com` launcher, and the
PowerShell install/uninstall scripts. The installer creates desktop and Start
Menu shortcuts and asks whether to enable autostart, recommending yes.

## CI

- `.github/workflows/ci.yml` runs non-GUI Go tests on GitHub-hosted runners.
  No self-hosted GitHub runner is required for this public repository.
- `.gitea/workflows/ci.yml` is the same test for a Gitea `act_runner` using the
  `ubuntu-latest` label.
- `.github/workflows/build.yml` is a manual workflow for Linux and Windows
  artifacts.

Self-hosted runners should only be attached to private/trusted repositories:
a public repository lets pull requests run code on the runner.

## License

MIT. The upstream Magpie copyright and this fork's copyright are both kept in
`LICENSE`.