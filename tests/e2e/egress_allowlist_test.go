package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestEgressAllowlistIsEnforcedAtHostKernel
//
// PERSONA
//   Alex, security-conscious OSS contributor. Considers running coding
//   agents on the codebase but has a hard requirement: a prompt-injected
//   or malicious agent must not be able to exfiltrate the repo to an
//   arbitrary internet host. The README's "kernel-level egress allowlist"
//   claim is the sole reason Alex is evaluating aa.
//
// JOURNEY
//   Alex configures aa with a narrow allowlist, starts a session, and
//   runs a battery of network probes from inside the container. For each,
//   the expected result is baked into the product's security story.
//
//   The allowlist for the test points at a local HTTP target hostname
//   `allowed.aa-test.localhost`. A second target, `blocked.aa-test.localhost`,
//   is explicitly NOT in the allowlist. Both resolve to loopback servers the
//   test owns, so nothing escapes the test host regardless of outcome.
//
//   Each probe runs INSIDE the aa-managed container, via a short-lived
//   one-shot agent command (`run: sh -c '<probe>; aa-done'`), so the probe
//   exercises the real enforced network path, not a simulated one.
//
//   Probes (each asserted by a sub-test):
//
//     P1. curl --proxy $HTTPS_PROXY https://allowed.aa-test.localhost/
//         EXPECT: succeeds with the target's "target:allowed" body.
//         WHY: the allowlisted host, via the proxy, is the happy path.
//
//     P2. curl --proxy $HTTPS_PROXY https://blocked.aa-test.localhost/
//         EXPECT: proxy returns 403.
//         WHY: the proxy must enforce the allowlist even for requests
//              politely going through it.
//
//     P3. HTTPS_PROXY= curl https://blocked.aa-test.localhost/
//         EXPECT: connection fails (timeout or refused) at the kernel, NOT
//                 a 403 from anywhere.
//         WHY: the container must not be able to bypass the proxy by
//              unsetting the env var and connecting directly.
//
//     P4. HTTPS_PROXY= curl https://allowed.aa-test.localhost/
//         EXPECT: also fails at the kernel.
//         WHY: even allowlisted hosts cannot be reached without the proxy;
//              the allowlist lives only at the proxy, not in the kernel
//              rule table.
//
//     P5. curl --resolve allowed.aa-test.localhost:443:<blocked-IP> \
//               --proxy $HTTPS_PROXY https://allowed.aa-test.localhost/
//         EXPECT: failure (proxy performs its own hostname resolution;
//                 client-side --resolve is ignored).
//         WHY: host-header spoofing cannot redirect traffic to an attacker-
//              controlled IP.
//
//     P6. getent hosts blocked.aa-test.localhost
//         EXPECT: no answer / failure.
//         WHY: DNS from inside the container must be blocked; exfil via
//              DNS tunneling is closed.
//
//     P7. dig @8.8.8.8 example.com
//         EXPECT: timeout / connection refused.
//         WHY: outbound DNS to public resolvers is blocked at the kernel.
//
//     P8. curl http://<raw-blocked-ip>/
//         EXPECT: dropped at the kernel.
//         WHY: raw-IP egress (no DNS, no proxy) is dropped — the rule is
//              destination-based, not hostname-based.
//
//     P9. After the agent runs `iptables -F` inside its own namespace:
//         re-run P3. EXPECT: still dropped.
//         WHY: the container is root in its own namespace, not on the
//              host; rules on the HOST kernel are outside its reach.
//
//     P10. curl --proxy http://<rogue-external-ip>:3128 \
//                https://allowed.aa-test.localhost/
//          EXPECT: dropped at kernel (can't reach the rogue proxy).
//          WHY: container cannot pivot through an attacker-controlled proxy
//               running elsewhere on the internet.
//
// BUSINESS IMPACT IF BROKEN
//   Any failure here directly contradicts the product's central security
//   claim. Alex (and every other security-conscious user) adopts aa because
//   of this guarantee, not in spite of it. A partial failure (e.g. DNS still
//   reachable) is still a total credibility loss: "mostly airgapped" is
//   indistinguishable from "not airgapped" in a threat model. The entire
//   product stands or falls on these ten assertions.
func TestEgressAllowlistIsEnforcedAtHostKernel(t *testing.T) {
	requireLinux(t)
	requireDocker(t)

	allowedURL := startLocalHTTPTarget(t, "allowed")
	blockedURL := startLocalHTTPTarget(t, "blocked")
	_ = allowedURL
	_ = blockedURL

	home := newIsolatedHome(t)

	// Configure a single-host allowlist. The implementation needs a way for
	// tests to pin a hostname to a local IP; the expected mechanism is an
	// `egress_test_resolve` map in the global config that the proxy honours
	// in test mode. If the real implementation exposes this via a different
	// hook, the test updates here — but the allowlist-vs-real-routing split
	// must be preserved.
	writeGlobalConfig(t, home, fmt.Sprintf(`{
  "default_backend": "local",
  "backends": {"local": {"type": "local", "egress_enforcement": "strict"}},
  "agents": {
    "probe-agent": {
      "run": "sh $AA_WORKSPACE/probe.sh",
      "env": {},
      "egress_allowlist": ["allowed.aa-test.localhost"],
      "egress_test_resolve": {
        "allowed.aa-test.localhost": %q,
        "blocked.aa-test.localhost": %q
      }
    }
  },
  "rules": []
}`, allowedURL, blockedURL))

	// The probe script emits a machine-readable result line per probe so the
	// test can parse outcomes deterministically. The aa implementation runs
	// this inside the container as the "agent" and the container's state
	// file captures the exit results.
	probeScript := `#!/bin/sh
set -u
exec > "$AA_WORKSPACE/.aa/probe-results.log" 2>&1

probe() {
  label=$1; shift
  if "$@" >/tmp/probe-out 2>/tmp/probe-err; then
    echo "PROBE $label OK $(cat /tmp/probe-out | head -c 200)"
  else
    echo "PROBE $label FAIL $(cat /tmp/probe-err | head -c 200)"
  fi
}

probe P1  curl -sS --max-time 5 --proxy "$HTTPS_PROXY" https://allowed.aa-test.localhost/
probe P2  curl -sS --max-time 5 --proxy "$HTTPS_PROXY" https://blocked.aa-test.localhost/
probe P3  env -u HTTPS_PROXY curl -sS --max-time 5 https://blocked.aa-test.localhost/
probe P4  env -u HTTPS_PROXY curl -sS --max-time 5 https://allowed.aa-test.localhost/
probe P5  curl -sS --max-time 5 --resolve allowed.aa-test.localhost:443:198.18.0.1 --proxy "$HTTPS_PROXY" https://allowed.aa-test.localhost/
probe P6  getent hosts blocked.aa-test.localhost
probe P7  dig +time=3 +tries=1 @8.8.8.8 example.com
probe P8  curl -sS --max-time 5 http://198.18.0.1/
iptables -F 2>/dev/null || true
probe P9  env -u HTTPS_PROXY curl -sS --max-time 5 https://blocked.aa-test.localhost/
probe P10 curl -sS --max-time 5 --proxy http://198.18.0.99:3128 https://allowed.aa-test.localhost/

touch "$AA_WORKSPACE/.aa/state"
echo DONE > "$AA_WORKSPACE/.aa/state"
`
	repo := newGitRepo(t, `{"image":".devcontainer/Dockerfile","agent":"probe-agent"}`)
	if err := writeFile(repo, "probe.sh", probeScript); err != nil {
		t.Fatalf("write probe.sh: %v", err)
	}

	// Start the session. Agent exits by itself after the probes complete.
	startRun := runAa(t, aaInvocation{
		Args:     []string{},
		HomeDir:  home,
		WorkDir:  repo,
		Deadline: 120 * time.Second,
	})
	// We don't care about aa's exit; we care about the probe results file.
	_ = startRun

	// Pull the probe log out of the (still-running or just-finished) container
	// via `aa attach --read .aa/probe-results.log` (interpreted relative to $AA_WORKSPACE) or an equivalent
	// docs-defined read path. For now: expect the file to exist on the host
	// via whatever read mechanism aa exposes. The assertion shape below
	// documents the contract every probe must satisfy.
	probeLog := readProbeResults(t, home, repo)

	expected := map[string]probeExpect{
		"P1":  {mustStart: "OK", mustMatch: "target:allowed"},
		"P2":  {mustStart: "FAIL", mustMatch: "403"},
		"P3":  {mustStart: "FAIL"},
		"P4":  {mustStart: "FAIL"},
		"P5":  {mustStart: "FAIL"},
		"P6":  {mustStart: "FAIL"},
		"P7":  {mustStart: "FAIL"},
		"P8":  {mustStart: "FAIL"},
		"P9":  {mustStart: "FAIL"},
		"P10": {mustStart: "FAIL"},
	}

	for id, want := range expected {
		t.Run(id, func(t *testing.T) {
			line := findProbeLine(probeLog, id)
			if line == "" {
				t.Fatalf("probe %s not found in result log:\n%s", id, probeLog)
			}
			parts := strings.SplitN(line, " ", 4)
			if len(parts) < 3 {
				t.Fatalf("probe %s malformed line: %q", id, line)
			}
			outcome := parts[2]
			if outcome != want.mustStart {
				t.Fatalf("probe %s: expected outcome %s, got %s (full: %q)",
					id, want.mustStart, outcome, line)
			}
			if want.mustMatch != "" && !strings.Contains(line, want.mustMatch) {
				t.Fatalf("probe %s: expected line to contain %q; got %q",
					id, want.mustMatch, line)
			}
		})
	}

	_ = runAa(t, aaInvocation{Args: []string{"kill"}, HomeDir: home, WorkDir: repo})
}

type probeExpect struct {
	mustStart string // "OK" or "FAIL"
	mustMatch string // substring that MUST appear on the result line
}

// findProbeLine returns the "PROBE <id> ..." line for the given id, or "".
func findProbeLine(log, id string) string {
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, "PROBE "+id+" ") {
			return l
		}
	}
	return ""
}

// readProbeResults retrieves the probe log from the agent host's workspace.
// The implementation must expose a way to read workspace files — `aa diff`
// reads result.patch the same way. This helper is the test's expectation
// that some such read path exists.
func readProbeResults(t *testing.T, home, repo string) string {
	t.Helper()
	// Expected contract: the aa state directory on the laptop side caches
	// a pulled copy of $AA_WORKSPACE/.aa/probe-results.log on final poll. If
	// the implementation chooses a different mechanism, update this helper.
	res := runAa(t, aaInvocation{
		Args:    []string{"attach", "--read", ".aa/probe-results.log"},
		HomeDir: home,
		WorkDir: repo,
	})
	if res.ExitCode != 0 {
		t.Fatalf("could not read probe results via aa attach --read: stderr=%q", res.Stderr)
	}
	return res.Stdout
}

// writeFile is a tiny fixture helper, scoped to this test file.
func writeFile(dir, name, content string) error {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("cat > %s/%s", dir, name))
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}
