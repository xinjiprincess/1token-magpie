package plugin

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// packagedRuntime is a read-only default supplied by the signed installer. It
// never creates plugins.json or replaces a user's configured package or Bun.
type packagedRuntime struct {
	Bun     string
	Plugins []Entry
	Targets map[string]string
}

var packaged struct {
	sync.Once
	runtime *packagedRuntime
}

func bundledRuntime() *packagedRuntime {
	packaged.Do(func() {
		exe, err := os.Executable()
		if err == nil {
			packaged.runtime = findBundledRuntime(exe)
		}
	})
	return packaged.runtime
}
func findBundledRuntime(exe string) *packagedRuntime {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	beside := filepath.Dir(exe)
	dirs := []string{filepath.Join(beside, "model-core"), filepath.Join(beside, "resources", "model-core")}
	if filepath.Base(beside) == "MacOS" && filepath.Base(filepath.Dir(beside)) == "Contents" {
		dirs = append([]string{filepath.Join(filepath.Dir(beside), "Resources", "model-core")}, dirs...)
	}
	for _, dir := range dirs {
		if bundle := loadBundledRuntime(dir); bundle != nil {
			return bundle
		}
	}
	return nil
}
func loadBundledRuntime(root string) *packagedRuntime {
	var manifest struct {
		Protocol   int    `json:"protocol"`
		BunVersion string `json:"bun_version"`
		Plugins    []struct {
			Name    string            `json:"name"`
			Version string            `json:"version"`
			Path    string            `json:"path"`
			Files   map[string]string `json:"files,omitempty"`
		} `json:"plugins"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Protocol != 1 || manifest.BunVersion != BunVersion {
		return nil
	}
	bun := filepath.Join(root, bunExe())
	if info, err := os.Stat(bun); err != nil || info.IsDir() {
		return nil
	}
	bundle := &packagedRuntime{Bun: bun, Targets: map[string]string{}}
	for _, entry := range manifest.Plugins {
		if !pkgName.MatchString(entry.Name) || entry.Version == "" || filepath.IsAbs(entry.Path) {
			return nil
		}
		target := filepath.Join(root, filepath.FromSlash(entry.Path))
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		raw, err := os.ReadFile(filepath.Join(target, "package.json"))
		if err != nil || json.Unmarshal(raw, &pkg) != nil || pkg.Name != entry.Name || pkg.Version != entry.Version {
			return nil
		}
		for name, want := range entry.Files {
			file := filepath.Join(target, filepath.FromSlash(name))
			relative, err := filepath.Rel(target, file)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil
			}
			raw, err := os.ReadFile(file)
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
				return nil
			}
		}
		bundle.Plugins = append(bundle.Plugins, Entry{Spec: entry.Name + "@" + entry.Version})
		bundle.Targets[entry.Name] = target
	}
	return bundle
}
func mergedBundledPlugins(list List, bundle *packagedRuntime) List {
	if bundle == nil {
		return list
	}
	list.Plugins = clonePlugins(list.Plugins)
	have := map[string]bool{}
	for _, entry := range list.Plugins {
		name := Name(entry.Spec)
		if IsPath(entry.Spec) {
			var pkg struct {
				Name string `json:"name"`
			}
			raw, _ := os.ReadFile(filepath.Join(strings.TrimPrefix(entry.Spec, "file://"), "package.json"))
			if json.Unmarshal(raw, &pkg) == nil && pkg.Name != "" {
				name = pkg.Name
			}
		}
		have[name] = true
	}
	for _, entry := range bundle.Plugins {
		if !have[Name(entry.Spec)] {
			list.Plugins = append(list.Plugins, entry)
		}
	}
	return list
}
func bundledTarget(spec string) string {
	bundle := bundledRuntime()
	if bundle == nil {
		return ""
	}
	target := bundle.Targets[Name(spec)]
	if target == "" {
		return ""
	}
	if Pinned(spec) {
		for _, entry := range bundle.Plugins {
			if Name(entry.Spec) == Name(spec) && entry.Spec != spec {
				return ""
			}
		}
	}
	return target
}
func bundledInUse(spec string) bool {
	target := bundledTarget(spec)
	return target != "" && Target(spec) == target
}
