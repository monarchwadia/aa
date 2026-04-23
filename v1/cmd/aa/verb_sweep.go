// verb_sweep.go — `aa sweep` adapter. Delegates to SessionManager.Sweep,
// which enumerates orphan backend hosts, orphan local records, and
// orphan ephemeral keys, then prompts per-orphan before destroying
// anything.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"fmt"
	"io"
)

// verbSweep invokes SessionManager.Sweep and prints the full SweepReport
// regardless of which orphans the user confirmed for destruction. The
// report gives the user a transcript of what was observed, so the next
// `aa sweep` can be reasoned about.
//
// Example:
//
//	code := verbSweep(ctx, sm, nil, os.Stdout, os.Stderr)
//	// prints the report and returns 0
func verbSweep(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	report, err := sm.Sweep(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	fmt.Fprintln(stdout, "sweep report:")
	fmt.Fprintf(stdout, "  orphan backend hosts: %d\n", len(report.OrphanHosts))
	for _, h := range report.OrphanHosts {
		fmt.Fprintf(stdout, "    - %s (%s)\n", h.Address, h.BackendType)
	}
	fmt.Fprintf(stdout, "  orphan local records: %d\n", len(report.OrphanSessionRecords))
	for _, rec := range report.OrphanSessionRecords {
		fmt.Fprintf(stdout, "    - %s (host %s)\n", rec.ID, rec.Host.Address)
	}
	fmt.Fprintf(stdout, "  orphan ephemeral keys: %d\n", len(report.OrphanEphemeralKeys))
	for _, k := range report.OrphanEphemeralKeys {
		fmt.Fprintf(stdout, "    - %s (%s)\n", k.ID, k.Provider)
	}
	return 0
}
