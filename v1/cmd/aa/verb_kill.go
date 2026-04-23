// verb_kill.go — `aa kill` adapter. Delegates to SessionManager.Kill,
// which tears down the backend, revokes the ephemeral key, and deletes
// the local session record.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// verbKill derives the session id and invokes SessionManager.Kill.
// With --host-only the backend host is torn down and the ephemeral key
// revoked, but the local session record is preserved — useful together
// with `aa push` to demonstrate the "trust only the laptop" invariant.
// Returns 0 on success, 1 on any error.
//
// Example:
//
//	code := verbKill(ctx, sm, nil, os.Stdout, os.Stderr)
//	// tears down container, key, and local record; returns 0
func verbKill(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("aa kill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var hostOnly bool
	fs.BoolVar(&hostOnly, "host-only", false, "tear down backend host and revoke key, but keep the local session record")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if hostOnly {
		if err := sm.KillHostOnly(ctx, id); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "aa kill --host-only: session %s host torn down; local record preserved\n", id)
		return 0
	}
	if err := sm.Kill(ctx, id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "aa kill: session %s torn down (container, key, session record)\n", id)
	return 0
}
