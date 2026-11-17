package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/managedstate"
)

type managedReply struct {
	io.ReadCloser
	mu     sync.Mutex
	tail   []byte
	line   []byte
	usage  map[string]json.RawMessage
	finish func(int, json.RawMessage, error)
	status int
	once   sync.Once
}

func (r *managedReply) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.mu.Lock()
		r.tail = append(r.tail, p[:n]...)
		r.line = append(r.line, p[:n]...)
		for {
			at := bytes.IndexByte(r.line, '\n')
			if at < 0 {
				break
			}
			line := r.line[:at]
			if bytes.HasPrefix(line, []byte("data:")) {
				r.usage = mergeManagedUsage(r.usage, bytes.TrimSpace(line[5:]))
			}
			r.line = r.line[at+1:]
		}
		if len(r.line) > 128<<10 {
			r.line = nil
		}
		if len(r.tail) > 128<<10 {
			r.tail = append([]byte(nil), r.tail[len(r.tail)-(128<<10):]...)
		}
		r.mu.Unlock()
	}
	if err != nil {
		r.done(err)
	}
	return n, err
}
func (r *managedReply) Close() error { err := r.ReadCloser.Close(); r.done(err); return err }
func (r *managedReply) done(err error) {
	r.once.Do(func() {
		r.mu.Lock()
		tail := append([]byte(nil), r.tail...)
		r.tail = nil
		usage := mergeManagedUsage(r.usage, tail)
		r.mu.Unlock()
		if err == io.EOF {
			err = nil
		}
		var raw json.RawMessage
		if len(usage) > 0 {
			raw, _ = json.Marshal(usage)
		}
		r.finish(r.status, raw, err)
	})
}
func mergeManagedUsage(found map[string]json.RawMessage, data []byte) map[string]json.RawMessage {
	var node map[string]json.RawMessage
	if json.Unmarshal(data, &node) != nil {
		return found
	}
	take := func(raw json.RawMessage) {
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) != nil {
			return
		}
		if found == nil {
			found = map[string]json.RawMessage{}
		}
		for k, v := range value {
			found[k] = v
		}
	}
	take(node["usage"])
	take(node["usageMetadata"])
	for _, key := range []string{"response", "message"} {
		var child map[string]json.RawMessage
		if json.Unmarshal(node[key], &child) == nil {
			take(child["usage"])
			take(child["usageMetadata"])
		}
	}
	return found
}
func managedUsage(data []byte) json.RawMessage {
	found := mergeManagedUsage(nil, data)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("data:")) {
			found = mergeManagedUsage(found, bytes.TrimSpace(line[5:]))
		}
	}
	if len(found) == 0 {
		return nil
	}
	raw, _ := json.Marshal(found)
	return raw
}

func managedPayload(req *http.Request, body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(body, &payload)
	var model string
	_ = json.Unmarshal(payload["model"], &model)
	if model == "" {
		model = managedstate.Model(req.Context())
	}
	if model == "" {
		if _, after, ok := strings.Cut(req.URL.Path, "/models/"); ok {
			model, _, _ = strings.Cut(after, ":")
		}
	}
	if model == "" {
		med, params, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if med == "multipart/form-data" {
			reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				part, err := reader.NextPart()
				if err != nil {
					break
				}
				if part.FormName() == "model" {
					value, _ := io.ReadAll(io.LimitReader(part, 4096))
					model = string(value)
					break
				}
				part.Close()
			}
		}
	}
	if model == "" {
		return nil, fmt.Errorf("model attempt has no identifiable model")
	}
	if payload == nil {
		payload = map[string]json.RawMessage{}
	}
	payload["model"], _ = json.Marshal(model)
	return json.Marshal(payload)
}

func (p Provider) managedDo(client *http.Client, req *http.Request) (*http.Response, error) {
	var body []byte
	if req.GetBody != nil {
		copy, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		body, err = io.ReadAll(io.LimitReader(copy, 32<<20))
		copy.Close()
		if err != nil {
			return nil, err
		}
	}
	if managedstate.Invocation(req.Context()) == "" {
		return p.do(client, req)
	}
	admission, err := managedPayload(req, body)
	if err != nil {
		managedstate.CancelInvocation(req.Context())
		return nil, err
	}
	finish, err := managedstate.Admit(req.Context(), p.ID, admission)
	if err != nil {
		return nil, err
	}
	response, err := p.do(client, req)
	if err != nil {
		finish(0, nil, err)
		return response, err
	}
	if response.Body == nil {
		finish(response.StatusCode, nil, nil)
		return response, nil
	}
	response.Body = &managedReply{ReadCloser: response.Body, status: response.StatusCode, finish: finish}
	return response, nil
}
