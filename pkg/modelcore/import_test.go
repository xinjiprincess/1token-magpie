package modelcore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseImportLinkKeepsKeysOnlyInManifest(t *testing.T) {
	result, err := parseImport(json.RawMessage(`{"link":"magpie://import?name=Fixture&chat=https%3A%2F%2Fmodel.invalid%2Fv1&key=fixture-secret&models=model-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	data := result.(map[string]any)
	preview, _ := json.Marshal(data["preview"])
	manifest, _ := json.Marshal(data["manifest"])
	if strings.Contains(string(preview), "fixture-secret") || !strings.Contains(string(manifest), "fixture-secret") {
		t.Fatal("import preview/manifest credentials contract violated")
	}
	for _, link := range []string{"https://model.invalid", "magpie://import?name=NoKey&chat=https://model.invalid", "magpie://import?name=Bad&chat=https://user:private-secret@model.invalid&key=fixture-secret"} {
		raw, _ := json.Marshal(map[string]string{"link": link})
		_, err := parseImport(raw)
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unsafe import accepted or echoed")
		}
	}
}
