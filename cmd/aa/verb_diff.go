// verb_diff.go — `aa diff` adapter. Pulls the patch bytes from the
// backend via SessionManager.Diff and writes them to stdout, suitable
// for piping through `$PAGER`.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"fmt"
	"io"
)

// verbDiff derives the current session id, fetches patch bytes via
// SessionManager.Diff, and writes them to stdout. Returns 0 on success,
// 1 on any error.
//
// Example:
//
//	code := verbDiff(ctx, sm, nil, os.Stdout, os.Stderr)
//	// prints patch contents to stdout, returns 0
func verbDiff(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	data, err := sm.Diff(ctx, id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := stdout.Write(data); err != nil {
		fmt.Fprintf(stderr, "aa diff: write output: %v\n", err)
		return 1
	}
	return 0
}
