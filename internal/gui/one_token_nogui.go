//go:build nogui

package gui

import "net/http"

// The 1token account page belongs to the desktop shell; a nogui build has no
// page that could call it, but api.go still registers the routes.
func oneTokenRoutes(*http.ServeMux, Windows) {}
