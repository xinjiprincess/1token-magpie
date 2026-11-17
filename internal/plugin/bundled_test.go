package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeBundleFixture(t *testing.T, root string) {
	t.Helper()
	pkg := filepath.Join(root, "plugins", "node_modules", "@fixture", "auth")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@fixture/auth","version":"1.2.3"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, bunExe()), nil, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"protocol": 1, "bun_version": BunVersion, "plugins": []map[string]string{{"name": "@fixture/auth", "version": "1.2.3", "path": "plugins/node_modules/@fixture/auth"}}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}
func TestBundledRuntimeLocationsAndExplicitPluginWins(t *testing.T) {
	for _, tc := range []struct{ name, exe, resources string }{
		{"mac", "Magpie.app/Contents/MacOS/magpie", "Magpie.app/Contents/Resources/model-core"},
		{"windows-resources", "magpie.exe", "resources/model-core"},
		{"beside", "magpie", "model-core"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			resources := filepath.Join(root, filepath.FromSlash(tc.resources))
			writeBundleFixture(t, resources)
			bundle := findBundledRuntime(filepath.Join(root, filepath.FromSlash(tc.exe)))
			if bundle == nil || bundle.Bun != filepath.Join(resources, bunExe()) {
				t.Fatalf("resource lookup=%+v", bundle)
			}
			empty := mergedBundledPlugins(List{}, bundle)
			if len(empty.Plugins) != 1 || empty.Plugins[0].Spec != "@fixture/auth@1.2.3" {
				t.Fatalf("defaults=%+v", empty)
			}
			explicit := List{Plugins: []Entry{{Spec: "@fixture/auth@1.0.0", Off: true}}}
			merged := mergedBundledPlugins(explicit, bundle)
			if len(merged.Plugins) != 1 || merged.Plugins[0].Spec != "@fixture/auth@1.0.0" || !merged.Plugins[0].Off {
				t.Fatalf("user override replaced=%+v", merged)
			}
			if _, err := os.Stat(filepath.Join(root, "plugins.json")); !os.IsNotExist(err) {
				t.Fatal("resource lookup wrote a user configuration")
			}
		})
	}
}
func TestBundledRuntimeRejectsEscapingOrWrongVersion(t *testing.T) {
	root := t.TempDir()
	writeBundleFixture(t, root)
	path := filepath.Join(root, "runtime.json")
	raw, _ := os.ReadFile(path)
	var manifest map[string]any
	_ = json.Unmarshal(raw, &manifest)
	manifest["plugins"] = []map[string]string{{"name": "@fixture/auth", "version": "1.2.3", "path": "../outside"}}
	changed, _ := json.Marshal(manifest)
	_ = os.WriteFile(path, changed, 0644)
	if loadBundledRuntime(root) != nil {
		t.Fatal("bundle escaped its installed resource directory")
	}
	_ = os.WriteFile(path, raw, 0644)
	_ = os.WriteFile(filepath.Join(root, "plugins", "node_modules", "@fixture", "auth", "package.json"), []byte(`{"name":"@fixture/auth","version":"0.0.0"}`), 0644)
	if loadBundledRuntime(root) != nil {
		t.Fatal("bundle ignored locked package version")
	}
}
