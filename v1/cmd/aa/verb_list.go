// verb_list.go — `aa list` adapter. Prints every locally-known session
// record, newest-first, with its displayed state.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"fmt"
	"io"
)

// verbList invokes SessionManager.ListAll, computes each session's state,
// and prints one line per session. Returns 0 on success, 1 on any error.
//
// The output has the shape:
//
//	<id>  <backend>  <branch>  <state>  <created-at>
//
// which makes `aa list | grep RUNNING` a useful scripting primitive
// (PHILOSOPHY axis 3: grep is the debugger).
//
// Example:
//
//	code := verbList(ctx, sm, nil, os.Stdout, os.Stderr)
//	// prints one line per session, returns 0
func verbList(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	records, err := sm.ListAll(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if len(records) == 0 {
		fmt.Fprintln(stdout, "(no sessions)")
		return 0
	}

	for _, rec := range records {
		state, _, err := sm.Status(ctx, rec.ID)
		if err != nil {
			state = SessionState("UNKNOWN")
		}
		fmt.Fprintf(stdout, "%s  %s  %s  %s  %s\n",
			rec.ID,
			rec.Backend,
			rec.Branch,
			state,
			rec.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		)
	}
	return 0
}
