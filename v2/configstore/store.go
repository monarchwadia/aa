// Package configstore — store.go handles on-disk persistence.
// File format: key=value lines, # comments and blank lines ignored.
// File mode 0600, parent dir 0700.
package configstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigPath returns the absolute path to the aa config file:
// $XDG_CONFIG_HOME/aa/config on Linux, following os.UserConfigDir semantics.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aa", "config"), nil
}

// Read loads the config file into a map. A missing file is NOT an error;
// it returns an empty map. Malformed lines (no `=`), blank lines, and
// comment lines (# prefix) are skipped silently.
func Read() (map[string]string, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return readFrom(path)
}

func readFrom(path string) (map[string]string, error) {
	cfg := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		// Preserve value literally (including embedded `=`), only trim
		// surrounding whitespace so trailing `\r` / spaces don't leak.
		cfg[k] = strings.TrimSpace(v)
	}
	return cfg, nil
}

// Write persists cfg to disk, creating the parent dir with 0700 and the
// file with 0600. Keys are written in sorted order for stable diffs.
func Write(cfg map[string]string) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Re-chmod the parent dir in case it pre-existed with looser perms.
	_ = os.Chmod(filepath.Dir(path), 0o700)

	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, cfg[k])
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
