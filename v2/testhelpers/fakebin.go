package testhelpers

// Fake external-binary plumbing. The sandbox plants a small shell script on
// PATH; each invocation appends one JSON record to the invocation log and
// emits the declared stdout/stderr before exiting with the declared code.
// The log is a stream of JSON objects (one per line), which readInvocations
// decodes back into []Invocation.

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeFakeBinary plants a fake executable at binDir/<b.Name>, configured to
// append one JSON record per invocation into logDir/<b.Name>.log and emit
// the declared stdout, stderr, and exit code.
//
// Example:
//
//	_ = writeFakeBinary("/tmp/bin", "/tmp/invocations", FakeBinary{
//	    Name: "flyctl", ExitCode: 0, Stdout: "ok\n",
//	})
func writeFakeBinary(binDir, logDir string, b FakeBinary) error {
	if b.Name == "" {
		return fmt.Errorf("writeFakeBinary: empty Name")
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("mkdir bin: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("mkdir log: %w", err)
	}

	logPath := filepath.Join(logDir, b.Name+".log")
	stdoutB64 := base64.StdEncoding.EncodeToString([]byte(b.Stdout))
	stderrB64 := base64.StdEncoding.EncodeToString([]byte(b.Stderr))

	// The script:
	//   1. Reads stdin (if any) into a temp file, then base64-encodes it.
	//   2. Emits one JSON record to the invocation log. argv is encoded as a
	//      base64 JSON array so characters like quotes and newlines survive.
	//   3. Writes the canned stdout/stderr (base64-decoded) and exits with
	//      the declared code.
	//
	// The argv-encoding dance: we pipe each arg through base64 and build a
	// JSON array in shell. We avoid python/jq because stdlib-only means
	// stdlib-only inside the fake too.
	script := fmt.Sprintf(`#!/bin/sh
set -e

LOG_FILE=%q
STDOUT_B64=%q
STDERR_B64=%q
EXIT_CODE=%d

# Capture stdin to a temp file (may be empty).
STDIN_TMP=$(mktemp)
trap 'rm -f "$STDIN_TMP"' EXIT
cat > "$STDIN_TMP" || true
STDIN_B64=$(base64 < "$STDIN_TMP" | tr -d '\n')

# Build a base64-encoded argv JSON array: ["<b64a0>","<b64a1>",...]
ARGV_JSON='['
FIRST=1
for a in "$@"; do
  AB=$(printf '%%s' "$a" | base64 | tr -d '\n')
  if [ $FIRST -eq 1 ]; then
    ARGV_JSON="$ARGV_JSON\"$AB\""
    FIRST=0
  else
    ARGV_JSON="$ARGV_JSON,\"$AB\""
  fi
done
ARGV_JSON="$ARGV_JSON]"

# Append one record. env is not captured in this fake — meta-tests do not
# require it and the Invocation.Env field is populated as an empty map.
printf '{"argv_b64":%%s,"stdin_b64":"%%s"}\n' "$ARGV_JSON" "$STDIN_B64" >> "$LOG_FILE"

# Emit canned stdout/stderr.
if [ -n "$STDOUT_B64" ]; then
  printf '%%s' "$STDOUT_B64" | base64 -d
fi
if [ -n "$STDERR_B64" ]; then
  printf '%%s' "$STDERR_B64" | base64 -d 1>&2
fi

exit $EXIT_CODE
`, logPath, stdoutB64, stderrB64, b.ExitCode)

	scriptPath := filepath.Join(binDir, b.Name)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		return fmt.Errorf("write fake script: %w", err)
	}
	return nil
}

// readInvocations parses the invocation log for the named fake binary and
// returns the ordered list of observed calls. Returns an empty slice if the
// binary was never invoked.
//
// Example:
//
//	invs, _ := readInvocations("/tmp/invocations", "flyctl")
//	// len(invs) == number of times aa shelled out to flyctl
func readInvocations(logDir, name string) ([]Invocation, error) {
	logPath := filepath.Join(logDir, name+".log")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []Invocation{}, nil
		}
		return nil, fmt.Errorf("open invocation log: %w", err)
	}
	defer f.Close()

	var invs []Invocation
	scanner := bufio.NewScanner(f)
	// Some invocations (large stdin) could exceed the default 64KiB line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record struct {
			ArgvB64  []string `json:"argv_b64"`
			StdinB64 string   `json:"stdin_b64"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("parse invocation log line: %w", err)
		}
		argv := make([]string, 0, len(record.ArgvB64))
		for _, ab := range record.ArgvB64 {
			dec, err := base64.StdEncoding.DecodeString(ab)
			if err != nil {
				return nil, fmt.Errorf("decode argv arg: %w", err)
			}
			argv = append(argv, string(dec))
		}
		stdin, err := base64.StdEncoding.DecodeString(record.StdinB64)
		if err != nil {
			return nil, fmt.Errorf("decode stdin: %w", err)
		}
		invs = append(invs, Invocation{
			Argv:  argv,
			Env:   map[string]string{},
			Stdin: stdin,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan invocation log: %w", err)
	}
	return invs, nil
}
