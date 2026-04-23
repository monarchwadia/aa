// verb_push.go — `aa push` adapter. Delegates to SessionManager.Push,
// whose body fetches the patch, runs rules, prompts per violation,
// applies locally, git-pushes, and tears down.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"fmt"
	"io"
)

// verbPush derives the session id and invokes SessionManager.Push.
// Returns 0 on success, 1 on any error (including user abort on a rule
// violation).
//
// Example:
//
//	code := verbPush(ctx, sm, nil, os.Stdout, os.Stderr)
//	// runs the full push pipeline and returns 0 on success
func verbPush(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := sm.Push(ctx, id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "aa push: session %s shipped\n", id)
	return 0
}
