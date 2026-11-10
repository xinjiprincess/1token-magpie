package gui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOneTokenKeyOnlyLoopback(t *testing.T) {
	mux := http.NewServeMux()
	oneTokenKeyRoutes(mux)
	post := func(addr, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/plugin-key", strings.NewReader(body))
		r.RemoteAddr = addr
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := post("192.0.2.1:1234", `{"provider":"1token"}`); w.Code != http.StatusForbidden {
		t.Fatalf("a remote caller got %d, want 403", w.Code)
	}
	if w := post("127.0.0.1:1234", "{}"); w.Code != http.StatusBadRequest {
		t.Fatalf("a missing provider got %d, want 400", w.Code)
	}
}
