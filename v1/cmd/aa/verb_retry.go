// verb_retry.go — `aa retry` adapter. Delegates to SessionManager.Retry,
// which refuses unless the session is in LIMBO or FAILED and otherwise
// invokes Backend.RunContainer on the existing Host.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"fmt"
	"io"
)

// verbRetry derives the session id and invokes SessionManager.Retry.
// Returns 0 on success, 1 on any error (including wrong-state refusal).
//
// Example:
//
//	code := verbRetry(ctx, sm, nil, os.Stdout, os.Stderr)
//	// restarts container in existing workspace, returns 0
func verbRetry(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := sm.Retry(ctx, id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "aa retry: session %s restarted\n", id)
	return 0
}
