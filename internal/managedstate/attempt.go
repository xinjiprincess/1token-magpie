package managedstate

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
)

type invocationKey struct{}

func WithInvocation(ctx context.Context, ticket string) context.Context {
	return context.WithValue(ctx, invocationKey{}, ticket)
}
func Invocation(ctx context.Context) string {
	ticket, _ := ctx.Value(invocationKey{}).(string)
	return ticket
}

type AttemptHost interface {
	Admit(context.Context, string, []byte) (func(int, json.RawMessage, error), error)
	Invoke(context.Context, string, *http.Request) (*http.Response, error)
}

func attemptHost() AttemptHost {
	state.RLock()
	defer state.RUnlock()
	host, _ := state.store.(AttemptHost)
	return host
}
func Admit(ctx context.Context, provider string, body []byte) (func(int, json.RawMessage, error), error) {
	if Invocation(ctx) == "" {
		return func(int, json.RawMessage, error) {}, nil
	}
	if host := attemptHost(); host != nil {
		return host.Admit(ctx, provider, body)
	}
	return func(int, json.RawMessage, error) {}, nil
}
func Invoke(ctx context.Context, provider string, request *http.Request) (*http.Response, error) {
	return attemptHost().Invoke(ctx, provider, request)
}

type invocationCancelKey struct{}

func WithInvocationCancel(ctx context.Context, ticket string, cancel context.CancelFunc) context.Context {
	return context.WithValue(context.WithValue(WithInvocation(ctx, ticket), invocationCancelKey{}, cancel), authorizationKey{}, &Authorization{})
}
func CancelInvocation(ctx context.Context) {
	if cancel, ok := ctx.Value(invocationCancelKey{}).(context.CancelFunc); ok {
		cancel()
	}
}

// WithModel carries a protocol's selected model when its wire body omits one.
type modelKey struct{}

func WithModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, modelKey{}, model)
}
func Model(ctx context.Context) string { model, _ := ctx.Value(modelKey{}).(string); return model }

// Authorization is scoped to one inference, shared with its classifiers/retries.
type Authorization struct {
	Allowed atomic.Bool
	Denied  atomic.Bool
}
type authorizationKey struct{}

func AuthorizationOf(ctx context.Context) *Authorization {
	state, _ := ctx.Value(authorizationKey{}).(*Authorization)
	return state
}
func Denied(ctx context.Context) {
	if state := AuthorizationOf(ctx); state != nil {
		state.Denied.Store(true)
	}
}
func Allowed(ctx context.Context) {
	if state := AuthorizationOf(ctx); state != nil {
		state.Allowed.Store(true)
	}
}
