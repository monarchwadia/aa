// verb_init.go — `aa init` and `aa init --global` adapters.
//
// `aa init` writes a minimal `aa.json` at the current working directory
// (the conventional repo root). `aa init --global` writes a starter
// `~/.aa/config.json` with the default rule block from README § "aa init
// defaults".
//
// Neither path constructs a SessionManager — they are config-scaffold
// commands whose only dependency is the filesystem. This file is NOT in
// strict mode (see docs/PHILOSOPHY.md § "Strict mode").
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// starterRepoConfig is the exact text `aa init` writes to aa.json.
// Two fields, per README § "Repo config (aa.json)": image and agent.
const starterRepoConfig = `{
  "image": ".devcontainer/Dockerfile",
  "agent": "claude-code"
}
`

// starterGlobalConfig is the exact text `aa init --global` writes to
// ~/.aa/config.json. Mirrors README § "Global config" with the default
// rule block from README § "aa init defaults".
const starterGlobalConfig = `{
  "default_backend": "local",
  "backends": {
    "local": {
      "type": "local",
      "egress_enforcement": "strict"
    }
  },
  "agents": {
    "claude-code": {
      "run": "claude --dangerously-skip-permissions",
      "env": {
        "ANTHROPIC_API_KEY": "keyring:anthropic"
      },
      "egress_allowlist": ["api.anthropic.com"]
    }
  },
  "rules": [
    { "type": "gitHooksChanged",        "severity": "error" },
    { "type": "ciConfigChanged",        "severity": "error" },
    { "type": "packageManifestChanged", "severity": "warn"  },
    { "type": "lockfileChanged",        "severity": "warn"  },
    { "type": "dockerfileChanged",      "severity": "warn"  },
    { "type": "buildScriptChanged",     "severity": "warn"  }
  ]
}
`

// verbInit parses the --global flag and dispatches to the repo or global
// scaffolding path. Returns 0 on success, 1 on any filesystem error.
//
// Example:
//
//	code := verbInit([]string{"--global"}, os.Stdout, os.Stderr)
//	// writes ~/.aa/config.json if absent, returns 0
func verbInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("aa init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var global bool
	fs.BoolVar(&global, "global", false, "write the starter global config at ~/.aa/config.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if global {
		return writeStarterGlobal(stdout, stderr)
	}
	return writeStarterRepo(stdout, stderr)
}

// writeStarterRepo writes aa.json in the current working directory.
// Refuses to overwrite an existing file — axis 3 observability says the
// user should always know their configured state is preserved.
func writeStarterRepo(stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "aa init: getwd: %v\n", err)
		return 1
	}
	path := filepath.Join(cwd, "aa.json")
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(stderr, "aa init: %s already exists; refusing to overwrite\n", path)
		return 1
	}
	if err := os.WriteFile(path, []byte(starterRepoConfig), 0o644); err != nil {
		fmt.Fprintf(stderr, "aa init: write %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintf(stdout, "aa init: wrote aa.json at %s\n", path)
	return 0
}

// writeStarterGlobal writes ~/.aa/config.json with the starter content.
// Refuses to overwrite; creates the ~/.aa directory if absent.
func writeStarterGlobal(stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "aa init --global: locate home: %v\n", err)
		return 1
	}
	dir := filepath.Join(home, ".aa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "aa init --global: mkdir %s: %v\n", dir, err)
		return 1
	}
	path := filepath.Join(dir, "config.json")
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(stderr, "aa init --global: %s already exists; refusing to overwrite\n", path)
		return 1
	}
	if err := os.WriteFile(path, []byte(starterGlobalConfig), 0o644); err != nil {
		fmt.Fprintf(stderr, "aa init --global: write %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintf(stdout, "aa init --global: wrote ~/.aa/config.json at %s\n", path)
	return 0
}
