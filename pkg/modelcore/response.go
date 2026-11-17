package modelcore

import (
	"bytes"
	"io"
	"net/http"

	"github.com/yetone/magpie/internal/managedstate"
)

// admissionResponse delays a streaming prelude until the parent authorizes the
// first actual attempt. A forged ticket must still receive an explicit 403 even
// if the gateway flushed its initial 200 before it selected a provider.
type admissionResponse struct {
	http.ResponseWriter
	state   *managedstate.Authorization
	status  int
	written bool
	pending bytes.Buffer
}

func (w *admissionResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= 400 {
		w.commit()
	}
}
func (w *admissionResponse) commit() {
	if w.written {
		return
	}
	w.written = true
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(status)
	if w.pending.Len() > 0 {
		_, _ = w.pending.WriteTo(w.ResponseWriter)
	}
}
func (w *admissionResponse) Write(data []byte) (int, error) {
	if !w.written && !w.state.Allowed.Load() {
		if w.pending.Len()+len(data) > 64<<10 {
			return 0, io.ErrShortBuffer
		}
		return w.pending.Write(data)
	}
	w.commit()
	return w.ResponseWriter.Write(data)
}
func (w *admissionResponse) Flush() {
	if !w.written && !w.state.Allowed.Load() {
		return
	}
	w.commit()
	if flush, ok := w.ResponseWriter.(http.Flusher); ok {
		flush.Flush()
	}
}
func (w *admissionResponse) finish() {
	if !w.written && w.state.Denied.Load() {
		w.pending.Reset()
		w.status = http.StatusForbidden
		w.Header().Set("Content-Type", "application/json")
		w.commit()
		_, _ = io.WriteString(w.ResponseWriter, `{"error":{"code":"INVOCATION_DENIED","message":"inference authorization denied"}}`)
		return
	}
	w.commit()
}
