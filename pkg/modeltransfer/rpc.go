package modeltransfer

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type response struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// Call supplies the model-transfer operations to the managed core as well as
// the standalone, read-only Magpie source helper. Passwords exist in this
// backend pipe only, never command arguments or environment variables.
func Call(method string, params json.RawMessage, appVersion string) (any, error) {
	switch method {
	case "describe":
		return map[string]any{"protocol": 1, "purpose": Purpose, "version": Version, "methods": []string{"describe", "list", "export", "apps.list", "apps.export", "seal", "open", "validate"}, "login_transfer_supported": false}, nil
	case "apps.list", "apps.export":
		var in struct {
			SourceID string `json:"source_id"`
			Selection
		}
		if json.Unmarshal(params, &in) != nil {
			return nil, ErrInvalid
		}
		m, ignored, err := CollectApp(in.SourceID, appVersion)
		if err != nil {
			return nil, err
		}
		if method == "apps.export" {
			return Select(m, in.Selection)
		}
		preview, err := Inspect(m)
		return map[string]any{"preview": preview, "ignored": ignored}, err
	case "list":
		m, err := Collect(appVersion)
		if err != nil {
			return nil, err
		}
		return Inspect(m)
	case "export":
		var selected Selection
		if json.Unmarshal(params, &selected) != nil {
			return nil, ErrInvalid
		}
		m, err := Collect(appVersion)
		if err != nil {
			return nil, err
		}
		return Select(m, selected)
	case "seal":
		var in struct {
			Manifest json.RawMessage `json:"manifest"`
			Password string          `json:"password"`
		}
		if json.Unmarshal(params, &in) != nil {
			return nil, ErrInvalid
		}
		m, err := Decode(in.Manifest)
		if err != nil {
			return nil, err
		}
		data, err := Seal(m, in.Password)
		if err != nil {
			return nil, err
		}
		return map[string]string{"data": base64.StdEncoding.EncodeToString(data)}, nil
	case "open":
		var in struct {
			Data     string `json:"data"`
			Password string `json:"password"`
		}
		if json.Unmarshal(params, &in) != nil {
			return nil, ErrInvalid
		}
		data, err := base64.StdEncoding.DecodeString(in.Data)
		if err != nil {
			return nil, ErrInvalid
		}
		return Open(data, in.Password)
	case "validate":
		var in struct {
			Manifest json.RawMessage `json:"manifest"`
		}
		if json.Unmarshal(params, &in) != nil {
			return nil, ErrInvalid
		}
		m, err := Decode(in.Manifest)
		if err != nil {
			return nil, err
		}
		p, err := Inspect(m)
		return map[string]any{"manifest": m, "preview": p}, err
	}
	return nil, fmt.Errorf("MODEL_TRANSFER_METHOD_UNSUPPORTED")
}

// Serve owns stdout until EOF. It intentionally has no process startup,
// subscription discovery, migration or background refresh of its own.
func Serve(input io.Reader, output io.Writer, appVersion string) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), MaxBytes)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var in request
		if json.Unmarshal(scanner.Bytes(), &in) != nil || len(in.ID) == 0 {
			return ErrInvalid
		}
		value, err := Call(in.Method, in.Params, appVersion)
		out := response{ID: in.ID, Result: value}
		if err != nil {
			code := "MODEL_TRANSFER_INVALID"
			if err == ErrChanged {
				code = "MODEL_TRANSFER_CHANGED"
			}
			if err == ErrSelection {
				code = "MODEL_TRANSFER_SELECTION_INVALID"
			}
			out.Result = nil
			out.Error = &rpcError{Code: code, Message: code}
		}
		if err := encoder.Encode(out); err != nil {
			return err
		}
	}
	return scanner.Err()
}
