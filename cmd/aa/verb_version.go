// verb_version.go — `aa version` adapter. Prints the binary's version
// string. No collaborator wiring is needed.
//
// This file is NOT in strict mode — it's a CLI adapter that prints one
// constant. See docs/PHILOSOPHY.md § "Strict mode".
package main

import (
	"fmt"
	"io"
)

// verbVersion writes the version constant to stdout and returns 0.
//
// Example:
//
//	code := verbVersion(os.Stdout)
//	// prints "aa v0.1.0-dev\n" and returns 0
func verbVersion(stdout io.Writer) int {
	fmt.Fprintln(stdout, aaVersion)
	return 0
}
