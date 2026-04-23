package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"aa/v2/configstore"
)

// configPath, readConfig, writeConfig are thin wrappers that forward to the
// configstore package; kept in this package because tests in the main
// package import them directly.
func configPath() (string, error)            { return configstore.ConfigPath() }
func readConfig() (map[string]string, error) { return configstore.Read() }
func writeConfig(cfg map[string]string) error {
	return configstore.Write(cfg)
}

// runConfig is the `aa config` CLI handler. Modes:
//   - list (no args):                         `aa config [--show-secrets]`
//   - remove (starts with --remove):          `aa config --remove <key> [<key>...]`
//   - set (any arg contains `=`):             `aa config k=v [k=v ...]`
//
// Validation-before-mutation: in set mode, if ANY arg is malformed the
// entire call fails with non-zero exit and nothing is persisted.
func runConfig(args []string) {
	// Separate into flags and positional.
	showSecrets := false
	removeMode := false
	positional := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "--show-secrets":
			showSecrets = true
		case "--remove":
			removeMode = true
		default:
			positional = append(positional, a)
		}
	}

	if removeMode {
		runConfigRemove(positional)
		return
	}

	if len(positional) == 0 {
		runConfigList(showSecrets)
		return
	}

	runConfigSet(positional)
}

func runConfigList(showSecrets bool) {
	cfg, err := readConfig()
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	if len(cfg) == 0 {
		fmt.Println("(no config set)")
		return
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := cfg[k]
		if !showSecrets && strings.HasPrefix(k, "token.") {
			v = "<set>"
		}
		fmt.Printf("%s=%s\n", k, v)
	}
}

func runConfigSet(pairs []string) {
	// Validate all pairs up-front; persist nothing unless all are valid.
	type kv struct{ k, v string }
	parsed := make([]kv, 0, len(pairs))
	for _, arg := range pairs {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			log.Fatalf("invalid config argument %q — expected key=value", arg)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			log.Fatalf("invalid config argument %q — expected key=value", arg)
		}
		parsed = append(parsed, kv{k: k, v: v})
	}

	cfg, err := readConfig()
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	for _, p := range parsed {
		cfg[p.k] = p.v
	}
	if err := writeConfig(cfg); err != nil {
		log.Fatalf("write config: %v", err)
	}
	// Acknowledge in input order, after successful persist.
	for _, p := range parsed {
		fmt.Printf("saved %s\n", p.k)
	}
}

func runConfigRemove(keys []string) {
	if len(keys) == 0 {
		fmt.Fprintln(os.Stderr, "usage: aa config --remove <key> [<key>...]")
		os.Exit(2)
	}
	cfg, err := readConfig()
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	for _, k := range keys {
		delete(cfg, k)
	}
	if err := writeConfig(cfg); err != nil {
		log.Fatalf("write config: %v", err)
	}
	for _, k := range keys {
		fmt.Printf("removed %s\n", k)
	}
}
