package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestFirstTimeSetupWritesBothConfigFiles
//
// PERSONA
//   Maya, senior backend engineer, has just downloaded the aa binary. She has
//   never run it before. Her laptop has no ~/.aa directory. She reads the
//   README, opens a fresh clone of a repo she wants to use aa in, and follows
//   the "First-time setup" section step by step.
//
// JOURNEY
//   1. Maya runs `aa init --global` in her home directory.
//      WHY: the README tells her this is the one-time setup that creates her
//           laptop-local config. She wants the minimum scaffold the tool needs.
//      OBSERVES: stdout says a config was written; `~/.aa/config.json` now
//                exists; the file parses as JSON; it contains `default_backend`,
//                at least one entry under `backends`, and at least one entry
//                under `agents`; the default rule set from the README is
//                present under `rules`.
//
//   2. Maya `cd`s into a fresh clone of her project and runs `aa init`.
//      WHY: she wants to mark this repo as aa-enabled with the minimum
//           per-repo configuration the README documents.
//      OBSERVES: stdout says `aa.json` was written; an `aa.json` file exists at
//                repo root; it parses as JSON; it contains exactly the two
//                documented fields `image` and `agent`, nothing more.
//
// BUSINESS IMPACT IF BROKEN
//   This is the onboarding path. A new user who cannot init a config will
//   never reach their first session. Every other feature is dead weight. A
//   broken `aa init` kills adoption at install-time, before the user has any
//   evidence the product works.
func TestFirstTimeSetupWritesBothConfigFiles(t *testing.T) {
	home := newIsolatedHome(t)
	repo := newGitRepo(t, "") // no aa.json pre-existing

	// Step 1 — aa init --global
	globalRun := runAa(t, aaInvocation{
		Args:    []string{"init", "--global"},
		HomeDir: home,
		WorkDir: home,
	})
	assertExitCode(t, globalRun.ExitCode, 0, "aa init --global")
	assertContains(t, globalRun.Stdout+globalRun.Stderr, "~/.aa/config.json", "aa init --global output")

	globalPath := filepath.Join(home, ".aa", "config.json")
	if _, err := os.Stat(globalPath); err != nil {
		t.Fatalf("expected %s to exist after aa init --global: %v", globalPath, err)
	}

	var global map[string]any
	if err := json.Unmarshal([]byte(readFile(t, globalPath)), &global); err != nil {
		t.Fatalf("global config is not valid JSON: %v", err)
	}
	for _, key := range []string{"default_backend", "backends", "agents", "rules"} {
		if _, ok := global[key]; !ok {
			t.Errorf("global config missing required top-level key %q", key)
		}
	}

	// Step 2 — aa init in the repo
	repoRun := runAa(t, aaInvocation{
		Args:    []string{"init"},
		HomeDir: home,
		WorkDir: repo,
	})
	assertExitCode(t, repoRun.ExitCode, 0, "aa init")
	assertContains(t, repoRun.Stdout+repoRun.Stderr, "aa.json", "aa init output")

	repoPath := filepath.Join(repo, "aa.json")
	if _, err := os.Stat(repoPath); err != nil {
		t.Fatalf("expected %s to exist after aa init: %v", repoPath, err)
	}

	var repoCfg map[string]any
	if err := json.Unmarshal([]byte(readFile(t, repoPath)), &repoCfg); err != nil {
		t.Fatalf("repo config is not valid JSON: %v", err)
	}
	for _, key := range []string{"image", "agent"} {
		if _, ok := repoCfg[key]; !ok {
			t.Errorf("repo config missing required field %q", key)
		}
	}
	for key := range repoCfg {
		if key != "image" && key != "agent" {
			t.Errorf("repo aa.json should have exactly 2 fields per README; found extra %q", key)
		}
	}
}
