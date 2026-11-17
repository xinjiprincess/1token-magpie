package modelcore

import (
	"encoding/json"
	"errors"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/pkg/modeltransfer"
)

// parseImport only parses the user's supplied link. No path, icon, model list,
// account or vendor endpoint is fetched until a reviewed connection is saved.
func parseImport(raw json.RawMessage) (any, error) {
	var input struct {
		Link string `json:"link"`
	}
	if err := decode(raw, &input); err != nil || len(input.Link) > 131072 {
		return nil, errors.New("invalid model import link")
	}
	p, err := provider.ParseImport(input.Link)
	if err != nil {
		return nil, errors.New("invalid model import link")
	}
	preset := provider.Preset(p.Preset)
	if p.Key == "" && (preset == nil || !preset.NoKey) {
		return nil, errors.New("model import link requires an API key")
	}
	m, err := modeltransfer.FromProvider(p, "magpie-import-link", "embedded")
	if err != nil {
		return nil, err
	}
	preview, err := modeltransfer.Inspect(m)
	if err != nil {
		return nil, err
	}
	return map[string]any{"manifest": m, "preview": preview}, nil
}
