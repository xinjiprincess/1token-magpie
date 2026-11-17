package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

var managed struct {
	sync.RWMutex
	path string
	set  bool
}

func ConfigureManaged(path string) error {
	if path != "" {
		info, err := os.Stat(path)
		if !filepath.IsAbs(path) || err != nil || info.IsDir() {
			return errors.New("bundled Claude Code is missing")
		}
	}
	managed.Lock()
	defer managed.Unlock()
	if managed.set {
		return errors.New("managed Claude Code already configured")
	}
	managed.path, managed.set = path, true
	return nil
}
func ManagedPath() (string, bool) {
	managed.RLock()
	defer managed.RUnlock()
	return managed.path, managed.set
}
