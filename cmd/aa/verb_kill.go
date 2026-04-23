// verb_kill.go — `aa kill` adapter. Delegates to SessionManager.Kill,
// which tears down the backend, revokes the ephemeral key, and deletes
// the local session record.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"fmt"
	"io"
)

// verbKill derives the session id and invokes SessionManager.Kill.
// Returns 0 on success, 1 on any error (including a dangling-backend
// error that the user must then sweep).
//
// Example:
//
//	code := verbKill(ctx, sm, nil, os.Stdout, os.Stderr)
//	// tears down container, key, and local record; returns 0
func verbKill(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := sm.Kill(ctx, id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "aa kill: session %s torn down (container, key, session record)\n", id)
	return 0
}
