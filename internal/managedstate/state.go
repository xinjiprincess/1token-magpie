// Package managedstate keeps model-core secret documents in its parent's vault.
// Ordinary Magpie never configures it and retains its existing storage behavior.
package managedstate

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

type Store interface {
	Get(context.Context, string) (json.RawMessage, error)
	Put(context.Context, string, json.RawMessage) error
}

var state struct {
	sync.RWMutex
	store  Store
	values map[string]json.RawMessage
}

// Configure must run before any provider or plugin is used in the managed process.
func Configure(store Store) error {
	state.Lock()
	defer state.Unlock()
	if state.store != nil {
		return errors.New("managed secret store already configured")
	}
	state.store = store
	state.values = make(map[string]json.RawMessage)
	return nil
}
func Enabled() bool { state.RLock(); defer state.RUnlock(); return state.store != nil }
func Read(ref string) ([]byte, error) {
	state.RLock()
	value, ok := state.values[ref]
	store := state.store
	state.RUnlock()
	if store == nil {
		return nil, errors.New("managed secret store not configured")
	}
	if ok {
		return append([]byte(nil), value...), nil
	}
	value, err := store.Get(context.Background(), "modelcore/"+ref)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 || string(value) == "null" {
		value = json.RawMessage(`{}`)
	}
	if !json.Valid(value) {
		return nil, errors.New("invalid managed secret document")
	}
	state.Lock()
	if kept, exists := state.values[ref]; exists {
		value = kept
	} else {
		state.values[ref] = append(json.RawMessage(nil), value...)
	}
	state.Unlock()
	return append([]byte(nil), value...), nil
}
func Write(ref string, value []byte) error {
	if !json.Valid(value) {
		return errors.New("invalid managed secret document")
	}
	state.RLock()
	store := state.store
	state.RUnlock()
	if store == nil {
		return errors.New("managed secret store not configured")
	}
	if err := store.Put(context.Background(), "modelcore/"+ref, json.RawMessage(value)); err != nil {
		return err
	}
	state.Lock()
	state.values[ref] = append(json.RawMessage(nil), value...)
	state.Unlock()
	return nil
}
