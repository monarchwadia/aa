// verb_status.go — `aa status` adapter. Renders the session's current
// state as a one-screen block, per README § "Session states". The block
// lists the state name, the agent's reported message (if any), the exit
// code, and the next-step verb suggestions appropriate to the state.
//
// This file is NOT in strict mode — it's a CLI adapter. See
// docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"context"
	"fmt"
	"io"
)

// verbStatus reads the current session id from git, queries the
// SessionManager, and prints the README-documented status block. Returns
// 0 on success, 1 on any error.
//
// Example:
//
//	code := verbStatus(ctx, sm, nil, os.Stdout, os.Stderr)
//	// prints the status block and returns 0
func verbStatus(ctx context.Context, sm *SessionManager, args []string, stdout, stderr io.Writer) int {
	id, err := currentSessionID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	state, remote, err := sm.Status(ctx, id)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	printStatusBlock(stdout, id, state, remote)
	return 0
}

// printStatusBlock renders one session's state to stdout in the exact
// shape README § "Session states" shows for each of the four terminal
// states plus RUNNING / PROVISIONING / PUSHED / TORN_DOWN.
func printStatusBlock(w io.Writer, id SessionID, state SessionState, remote RemoteStatus) {
	fmt.Fprintf(w, "  session %s — %s\n\n", id, state)
	switch state {
	case StateDone:
		fmt.Fprintln(w, "  agent reported success:")
		if remote.AgentMessage != "" {
			fmt.Fprintf(w, "    %q\n", remote.AgentMessage)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  next:")
		fmt.Fprintln(w, "    aa diff   review changes")
		fmt.Fprintln(w, "    aa push   ship to origin")
		fmt.Fprintln(w, "    aa kill   discard and tear down")
	case StateFailed:
		fmt.Fprintln(w, "  agent reported failure:")
		if remote.AgentMessage != "" {
			fmt.Fprintf(w, "    %q\n", remote.AgentMessage)
		}
		fmt.Fprintf(w, "\n  exit code: %d\n\n", remote.ExitCode)
		fmt.Fprintln(w, "  next:")
		fmt.Fprintln(w, "    aa attach  reattach to investigate (container still up)")
		fmt.Fprintln(w, "    aa diff    review partial changes")
		fmt.Fprintln(w, "    aa push    ship what's there (risky)")
		fmt.Fprintln(w, "    aa kill    discard and tear down")
	case StateLimbo:
		fmt.Fprintln(w, "  the agent process exited without reporting a result.")
		fmt.Fprintln(w, "  no state file was written. cause is unknown.")
		fmt.Fprintf(w, "\n  exit code: %d\n\n", remote.ExitCode)
		fmt.Fprintln(w, "  next:")
		fmt.Fprintln(w, "    aa attach  drop into shell to inspect workspace")
		fmt.Fprintln(w, "    aa diff    review any changes made before exit")
		fmt.Fprintln(w, "    aa push    ship what's there (risky — partial work)")
		fmt.Fprintln(w, "    aa kill    discard and tear down")
		fmt.Fprintln(w, "    aa retry   restart the agent in the same workspace")
	case StateInconsistent:
		fmt.Fprintf(w, "  the agent reported DONE but exited with code %d.\n", remote.ExitCode)
		fmt.Fprintln(w, "  this is unusual and may indicate a problem.")
		if remote.AgentMessage != "" {
			fmt.Fprintf(w, "\n  agent message:  %q\n", remote.AgentMessage)
		}
		fmt.Fprintf(w, "  exit code:      %d\n\n", remote.ExitCode)
		fmt.Fprintln(w, "  next:")
		fmt.Fprintln(w, "    aa attach  reattach to investigate")
		fmt.Fprintln(w, "    aa diff    review changes")
		fmt.Fprintln(w, "    aa push    trust the DONE, ship anyway")
		fmt.Fprintln(w, "    aa kill    discard and tear down")
	case StateRunning:
		fmt.Fprintln(w, "  agent is running. attach with `aa attach` to interact.")
	case StateProvisioning:
		fmt.Fprintln(w, "  provisioning... run `aa status` again in a moment.")
	case StatePushed:
		fmt.Fprintln(w, "  session already pushed. run `aa kill` to tear down if still present.")
	case StateTornDown:
		fmt.Fprintln(w, "  session already torn down. start a fresh one with `aa`.")
	default:
		fmt.Fprintf(w, "  (no rendering for state %q — README § Session states)\n", state)
	}
}
