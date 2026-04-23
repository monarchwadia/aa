// verb_fixture.go — `aa fixture` adapter (hidden, test-only).
//
// The fixture verb creates a complete session as if an agent had already
// run and written its results, WITHOUT actually executing any agent. It
// exists to let the e2e suite exercise post-agent journeys (`aa status`,
// `aa diff`, `aa push`) without spawning a real agent process and
// waiting for it to finish.
//
// Not exposed in `aa help`. Refuses to run unless
// AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 is set — the same env-var gate the
// process backend uses, so production users who haven't opted into the
// unsafe-laptop mode cannot accidentally trigger fixture scaffolding.
//
// This file is NOT in strict mode — it writes only into the workspace
// that Provision already created, under paths the real agent would also
// write (.aa/state, .aa/exit, .aa/result.patch). No user-supplied path
// leaves the workspace root.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// verbFixture implements `aa fixture`. Flags:
//
//	--state STRING     value to write into .aa/state (empty = don't write)
//	--exit  INT        value to write into .aa/exit  (-1    = don't write)
//	--message STRING   appended to --state as "STATE: <msg>" when both set
//	--patch-file PATH  copy bytes to .aa/result.patch when non-empty
//
// Derives the session id from cwd like every other verb. Provisions the
// backend, syncs the repo (for local-filesystem backends), writes the
// requested files, saves the LocalSessionRecord, and returns. No
// ephemeral key is minted — fixtures are test scaffolding, not real
// sessions, and tests that want a key mint it explicitly.
func verbFixture(ctx context.Context, sm *SessionManager, cfg Config, args []string, stdout, stderr io.Writer) int {
	if os.Getenv("AA_ALLOW_UNSAFE_PROCESS_BACKEND") != "1" {
		fmt.Fprintln(stderr,
			"aa fixture: refused; set AA_ALLOW_UNSAFE_PROCESS_BACKEND=1 to use — "+
				"this subcommand exists only to support the e2e test suite")
		return 1
	}

	fs := flag.NewFlagSet("aa fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		stateArg   string
		exitArg    int
		messageArg string
		patchFile  string
	)
	fs.StringVar(&stateArg, "state", "", "value for .aa/state; empty = do not write")
	fs.IntVar(&exitArg, "exit", -1, "value for .aa/exit; -1 = do not write")
	fs.StringVar(&messageArg, "message", "", "reason string appended to state as 'STATE: <message>'")
	fs.StringVar(&patchFile, "patch-file", "", "path to a patch whose bytes go to .aa/result.patch")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	repoRoot, branch, err := repoRootAndBranch()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	host, err := sm.Backend.Provision(ctx, id)
	if err != nil {
		fmt.Fprintf(stderr, "aa fixture: provision: %v\n", err)
		return 1
	}

	// Sync repo into local-filesystem workspaces so tests that poke
	// around the workspace see the same file shape as a real session.
	if host.Address == "" {
		if err := sm.syncRepoIntoWorkspace(repoRoot, host.Workspace); err != nil {
			fmt.Fprintf(stderr, "aa fixture: sync repo: %v\n", err)
			return 1
		}
	}

	aaDir := filepath.Join(host.Workspace, ".aa")
	if err := os.MkdirAll(aaDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "aa fixture: create .aa dir: %v\n", err)
		return 1
	}

	if stateArg != "" {
		content := stateArg
		if messageArg != "" {
			content = stateArg + ": " + messageArg
		}
		if err := os.WriteFile(filepath.Join(aaDir, "state"), []byte(content+"\n"), 0o644); err != nil {
			fmt.Fprintf(stderr, "aa fixture: write .aa/state: %v\n", err)
			return 1
		}
	}
	if exitArg >= 0 {
		if err := os.WriteFile(filepath.Join(aaDir, "exit"), []byte(fmt.Sprintf("%d\n", exitArg)), 0o644); err != nil {
			fmt.Fprintf(stderr, "aa fixture: write .aa/exit: %v\n", err)
			return 1
		}
	}
	if patchFile != "" {
		data, err := os.ReadFile(patchFile)
		if err != nil {
			fmt.Fprintf(stderr, "aa fixture: read patch file: %v\n", err)
			return 1
		}
		if err := os.WriteFile(filepath.Join(aaDir, "result.patch"), data, 0o644); err != nil {
			fmt.Fprintf(stderr, "aa fixture: write .aa/result.patch: %v\n", err)
			return 1
		}
	}

	rec := LocalSessionRecord{
		ID:        id,
		Repo:      repoRoot,
		Branch:    branch,
		Backend:   cfg.DefaultBackend,
		Host:      host,
		CreatedAt: sm.clockNow(),
	}
	if err := sm.Store.Save(rec); err != nil {
		fmt.Fprintf(stderr, "aa fixture: save record: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "aa fixture: seeded session %s in %s\n", id, host.Workspace)
	return 0
}
