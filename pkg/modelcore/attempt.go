package modelcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/managedstate"
)

func (c *Core) callback(id string) (chan request, func()) {
	ch := make(chan request, 64)
	c.mu.Lock()
	c.callbacks[id] = ch
	c.mu.Unlock()
	return ch, func() { c.mu.Lock(); delete(c.callbacks, id); c.mu.Unlock() }
}
func (c *Core) Admit(ctx context.Context, providerID string, body []byte) (func(int, json.RawMessage, error), error) {
	id := fmt.Sprintf("admit-%d", c.seq.Add(1))
	ch, remove := c.callback(id)
	defer remove()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	var modelID string
	_ = json.Unmarshal(payload["model"], &modelID)
	c.send(map[string]any{"event": "admission", "id": id, "method": "call.admit", "params": map[string]any{"invocation": managedstate.Invocation(ctx), "provider_id": providerID, "model_id": modelID, "payload": json.RawMessage(body)}})
	select {
	case response := <-ch:
		if response.Error != nil {
			managedstate.Denied(ctx)
			managedstate.CancelInvocation(ctx)
			return nil, errors.New("model attempt admission denied")
		}
		var result struct {
			OK     bool   `json:"ok"`
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(response.Result, &result) != nil || !result.OK || result.CallID == "" {
			managedstate.Denied(ctx)
			managedstate.CancelInvocation(ctx)
			return nil, errors.New("model attempt admission denied")
		}
		managedstate.Allowed(ctx)
		var once sync.Once
		return func(status int, usage json.RawMessage, err error) {
			once.Do(func() {
				value := map[string]any{"event": "attempt.finished", "call_id": result.CallID, "invocation": managedstate.Invocation(ctx), "provider_id": providerID, "model_id": modelID, "status": status, "usage": usage}
				if err != nil {
					value["failed"] = true
				}
				c.send(value)
			})
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

func (c *Core) Invoke(ctx context.Context, providerID string, req *http.Request) (*http.Response, error) {
	if managedstate.Invocation(ctx) == "" {
		return nil, errors.New("native bridge requires invocation authorization")
	}
	var body []byte
	if req.GetBody != nil {
		reader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		body, err = io.ReadAll(io.LimitReader(reader, MaxFrame))
		reader.Close()
		if err != nil {
			return nil, err
		}
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return nil, errors.New("invalid native bridge payload")
	}
	var modelID string
	_ = json.Unmarshal(payload["model"], &modelID)
	id := fmt.Sprintf("invoke-%d", c.seq.Add(1))
	ch, remove := c.callback(id)
	c.send(map[string]any{"event": "native.invoke", "id": id, "params": map[string]any{"invocation": managedstate.Invocation(ctx), "provider_id": providerID, "model_id": modelID, "payload": json.RawMessage(body), "window": 1 << 20}})
	var head request
	select {
	case head = <-ch:
	case <-ctx.Done():
		remove()
		c.send(map[string]any{"event": "native.cancel", "id": id})
		return nil, ctx.Err()
	case <-c.ctx.Done():
		remove()
		return nil, c.ctx.Err()
	}
	if head.Error != nil {
		remove()
		return nil, errors.New("native bridge refused request")
	}
	var result struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
	}
	if json.Unmarshal(head.Result, &result) != nil || result.Status < 100 || result.Status > 599 {
		remove()
		return nil, errors.New("invalid native bridge response")
	}
	reader, writer := io.Pipe()
	go func() {
		defer remove()
		defer writer.Close()
		for {
			select {
			case item := <-ch:
				if item.Error != nil {
					_ = writer.CloseWithError(errors.New("native bridge stream failed"))
					return
				}
				switch item.Event {
				case "data":
					bytes, err := base64.StdEncoding.DecodeString(item.Data)
					if err != nil || len(bytes) > 65536 {
						_ = writer.CloseWithError(errors.New("invalid native bridge chunk"))
						return
					}
					n, err := writer.Write(bytes)
					if err != nil {
						c.send(map[string]any{"event": "native.cancel", "id": id})
						return
					}
					c.send(map[string]any{"event": "native.credit", "id": id, "bytes": n})
				case "end":
					return
				}
			case <-ctx.Done():
				_ = writer.CloseWithError(ctx.Err())
				c.send(map[string]any{"event": "native.cancel", "id": id})
				return
			case <-c.ctx.Done():
				_ = writer.CloseWithError(c.ctx.Err())
				return
			}
		}
	}()
	headers := http.Header{}
	for key, value := range result.Headers {
		if !strings.EqualFold(key, "Set-Cookie") {
			headers.Set(key, value)
		}
	}
	return &http.Response{StatusCode: result.Status, Header: headers, Body: reader, Request: req}, nil
}
