package gui

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var (
	oneTokenWindowMu sync.Mutex
	oneTokenWindow   *application.WebviewWindow
)

// OpenLoginWindow is the top-level WebView2 window 1token's authorization
// page opens in. It is not an iframe: 1token sets X-Frame-Options: DENY.
func (h *host) OpenLoginWindow(link string) error {
	if h == nil || h.app == nil {
		return errNoLoginWindow
	}
	application.InvokeAsync(func() {
		oneTokenWindowMu.Lock()
		defer oneTokenWindowMu.Unlock()
		if oneTokenWindow != nil {
			oneTokenWindow.Close()
			oneTokenWindow = nil
		}
		win := h.app.Window.NewWithOptions(application.WebviewWindowOptions{
			URL:           link,
			Title:         "登录 1token",
			Width:         520,
			Height:        760,
			MinWidth:      420,
			MinHeight:     600,
			DisableResize: false,
		})
		win.Show()
		oneTokenWindow = win
	})
	return nil
}

func closeOneTokenWindow() {
	application.InvokeAsync(func() {
		oneTokenWindowMu.Lock()
		defer oneTokenWindowMu.Unlock()
		if oneTokenWindow != nil {
			oneTokenWindow.Close()
			oneTokenWindow = nil
		}
	})
}

var errNoLoginWindow = &loginWindowError{}

type loginWindowError struct{}

func (*loginWindowError) Error() string { return "the app window is not ready" }
