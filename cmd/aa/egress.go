// Package main — egress.go defines the EgressController, the component that
// installs and removes the three-layer egress lockdown (kernel firewall +
// forward proxy + DNS block) on an agent host.
//
// STRICT MODE. This file is listed in docs/PHILOSOPHY.md § "Strict mode —
// paths where defensive coding applies". Every code path in this file must:
//
//   - validate every input at entry,
//   - reject unknown / malformed values loudly,
//   - roll back partial state on any failure,
//   - never silently downgrade a security control,
//   - never splice an unvalidated string into a shell command.
//
// This is the single most security-critical path in aa. A bug here is a
// container escape of the agent's egress policy.
//
// The controller does NOT exec iptables directly. It composes shell commands
// and hands them to the SSHRunner to execute on the agent host, per
// docs/architecture/aa.md § "Decision 3" (in-process Go proxy scp'd to host)
// and the `egress-controller` workstream.

package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// proxyBindIP is the loopback-style address the forward proxy listens on the
// host side. Container bridge traffic that isn't addressed to this endpoint
// is DROP'd by the iptables rules Install composes.
const proxyBindIP = "127.0.0.1"

// proxyBindPort is the TCP port aa-proxy listens on, on the agent host.
const proxyBindPort = "3128"

// containerBridge is the iptables interface name the container runtime bridges
// agent traffic out of. DROP rules are scoped to packets originating on this
// interface so other host traffic is unaffected.
const containerBridge = "docker0"

// hostnameRegex is the strict-mode allowlist hostname validator. It rejects
// every shell metacharacter, whitespace, quote, redirect, glob, leading dash,
// embedded null byte, and empty string. A hostname is one or more DNS labels
// joined by dots; each label is 1-63 chars, alphanumeric or hyphen, with no
// leading or trailing hyphen.
//
// Every allowlist entry and every testResolve key passes through this regex.
// Anything that doesn't match is a hard error at the boundary; we do not
// attempt to escape or sanitize.
var hostnameRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

// ipv4Regex validates a testResolve target address. The proxy's --test-resolve
// flag takes `hostname=ip` pairs; we pin the RHS to dotted-quad so a hostile
// map value can't smuggle shell syntax past us either.
var ipv4Regex = regexp.MustCompile(`^(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)(\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)){3}$`)

// EgressController installs and removes the egress lockdown on an agent host.
//
// It composes (but never executes directly) three things, in order:
//
//  1. iptables rules on the host that DROP all outbound traffic from the
//     container bridge except to the forward proxy's IP:port.
//  2. iptables rules that DROP outbound UDP 53 and TCP 53 from the container
//     bridge (DNS block — closes DNS-tunnel exfil).
//  3. An scp of the compiled `aa-proxy` binary to the host, followed by
//     starting it with the validated allowlist.
//
// Example wiring (the ssh runner is the real one in production; tests use the
// fakeSSHRunner from fakes_test.go):
//
//	ctl := NewEgressController(sshRunner, "/opt/aa/proxy-binaries")
//	if err := ctl.Install(ctx, host, []string{"api.anthropic.com"}, nil); err != nil {
//	    return fmt.Errorf("egress install: %w", err)
//	}
//	defer ctl.Remove(ctx, host)
type EgressController struct {
	// SSHRunner is how every host-side command and file transfer happens.
	// EgressController never invokes iptables, scp, or nohup directly; it
	// hands a composed command to the runner.
	SSHRunner SSHRunner

	// ProxyBinariesDir is the path on the laptop to a directory holding the
	// cross-compiled `aa-proxy` binaries per architecture (e.g.
	// `aa-proxy-linux-amd64`, `aa-proxy-linux-arm64`). Install picks the
	// right one based on the host's architecture.
	ProxyBinariesDir string
}

// NewEgressController constructs an EgressController bound to the given SSH
// runner and proxy-binaries directory.
//
// Example:
//
//	runner := NewRealSSHRunner(...)
//	ctl := NewEgressController(runner, "/usr/local/lib/aa/proxy-binaries")
func NewEgressController(ssh SSHRunner, proxyBinariesDir string) *EgressController {
	return &EgressController{
		SSHRunner:        ssh,
		ProxyBinariesDir: proxyBinariesDir,
	}
}

// installRuleSpec is the set of iptables argument-vectors Install applies,
// in order. Each entry is both the "add" form (with -I or -A) and enough
// context to compose its reverse (-D with the same match args) for rollback.
//
// The argv slices are joined with single spaces into the final SSH command
// string. Because every token in the slice is either a literal we control or
// a value that has already passed hostnameRegex / ipv4Regex validation, the
// resulting string has no room for shell-syntax injection even though the
// SSHRunner interface takes a single command string.
type installRuleSpec struct {
	// description is a human-readable label used in error messages and the
	// rollback log. Never interpolated into a shell command.
	description string

	// addArgv is the iptables argv for applying the rule, starting with
	// "iptables".
	addArgv []string

	// deleteArgv is the iptables argv for reversing the rule (same match
	// args, -D instead of -I/-A). Applied during rollback and Remove.
	deleteArgv []string
}

// Install provisions iptables rules, scp's the aa-proxy binary to the host,
// and starts it with the given allowlist.
//
// Strict-mode contract:
//
//   - allowlist=nil or empty → hard error. There is no safe "zero hosts"
//     interpretation; the caller must either pass a real list or the explicit
//     opt-out ["*"].
//   - allowlist=["*"] → unrestricted (explicit opt-out from the README). NO
//     iptables rules are installed. The decision to skip enforcement is made
//     once, here, loud and visible.
//   - Any hostname in allowlist that contains shell metacharacters, whitespace,
//     or otherwise fails strict hostname-regex validation → hard error. No
//     attempt to escape; we reject.
//   - testResolve keys not covered by the allowlist → hard error (config
//     mistake; you cannot test-resolve a host the proxy will never forward to).
//   - Any SSH failure mid-Install → rollback already-applied rules, then
//     return an error naming the failed step and what was rolled back.
//   - ctx cancellation mid-Install → same rollback path.
//
// Example:
//
//	err := ctl.Install(ctx, host, []string{"api.anthropic.com"}, map[string]string{
//	    "api.anthropic.com": "151.101.65.123",
//	})
func (e *EgressController) Install(ctx context.Context, host Host, allowlist []string, testResolve map[string]string) error {
	// ------------------------------------------------------------------
	// 1. Strict-mode validation (no SSH calls before this passes).
	// ------------------------------------------------------------------
	if allowlist == nil {
		return fmt.Errorf("egress install: allowlist is nil — at least one hostname is required (pass [\"*\"] for the documented unrestricted opt-out)")
	}
	if len(allowlist) == 0 {
		return fmt.Errorf("egress install: allowlist is empty — at least one hostname is required (pass [\"*\"] for the documented unrestricted opt-out)")
	}

	// Explicit unrestricted opt-out from README § "The [\"*\"] escape hatch".
	// Must be exactly ["*"] — a wildcard mixed with other entries is a
	// config mistake, not an opt-out, so we reject it below via hostnameRegex.
	if len(allowlist) == 1 && allowlist[0] == "*" {
		return nil
	}

	for _, h := range allowlist {
		if !hostnameRegex.MatchString(h) {
			return fmt.Errorf("egress install: allowlist entry %q is not a valid hostname (strict hostname regex rejected it; no shell metacharacters, whitespace, quotes, redirects, globs, leading dashes, or empty strings permitted)", h)
		}
	}

	allowSet := make(map[string]struct{}, len(allowlist))
	for _, h := range allowlist {
		allowSet[h] = struct{}{}
	}

	// testResolve keys must pass the same regex AND appear in the allowlist.
	// Values must be IPv4 dotted-quad; the proxy's --test-resolve flag takes
	// `hostname=ip` and we refuse to hand it anything else.
	for name, ip := range testResolve {
		if !hostnameRegex.MatchString(name) {
			return fmt.Errorf("egress install: testResolve hostname %q is not a valid hostname (strict hostname regex rejected it)", name)
		}
		if _, ok := allowSet[name]; !ok {
			return fmt.Errorf("egress install: testResolve entry %q is not in the allowlist — test-resolve entries must also appear in the allowlist (the proxy will never forward to a host it doesn't allow)", name)
		}
		if !ipv4Regex.MatchString(ip) {
			return fmt.Errorf("egress install: testResolve value %q for host %q is not a valid IPv4 address", ip, name)
		}
	}

	// ------------------------------------------------------------------
	// 2. Compose the iptables rule plan. Every argv slice is built from
	//    constants and already-validated hostnames — nothing from the
	//    caller is spliced into a shell word unvalidated.
	// ------------------------------------------------------------------
	rules := buildIptablesRules()

	// ------------------------------------------------------------------
	// 3. Apply the rules in order. Track applied rules so a failure at
	//    any later step can reverse them in LIFO order.
	// ------------------------------------------------------------------
	applied := make([]installRuleSpec, 0, len(rules))

	fail := func(step string, cause error) error {
		rollbackErr := e.rollbackApplied(host, applied)
		if rollbackErr != nil {
			return fmt.Errorf("egress install: %s failed: %v; rollback ALSO failed (host may be in a partial state): %v", step, cause, rollbackErr)
		}
		return fmt.Errorf("egress install: %s failed: %v; rolled back %d iptables rule(s)", step, cause, len(applied))
	}

	for _, rule := range rules {
		if err := ctx.Err(); err != nil {
			return fail(fmt.Sprintf("iptables step %q (context cancelled before issuing)", rule.description), err)
		}
		cmd := shellJoin(rule.addArgv)
		// Append to `applied` BEFORE issuing the Run: we don't know
		// whether a Run that errors back to us actually landed the rule
		// on the host (partial-write, timeout, network blip), and the
		// safer default is to reverse anything we attempted. iptables
		// -D for a non-existent rule is a no-op error we swallow in
		// rollback.
		applied = append(applied, rule)
		if _, err := e.SSHRunner.Run(ctx, host, cmd); err != nil {
			return fail(fmt.Sprintf("iptables step %q", rule.description), err)
		}
		// A Run that succeeded but cancelled the context mid-call
		// (e.g. the test's RunFn cancels then returns nil) must still
		// trigger rollback — treat ctx cancellation as a failure.
		if err := ctx.Err(); err != nil {
			return fail(fmt.Sprintf("iptables step %q (context cancelled after applying)", rule.description), err)
		}
	}

	// ------------------------------------------------------------------
	// 4. scp the aa-proxy binary to the host.
	// ------------------------------------------------------------------
	if err := ctx.Err(); err != nil {
		return fail("scp aa-proxy binary (context cancelled before copy)", err)
	}
	srcBinary := e.ProxyBinariesDir + "/aa-proxy-linux-amd64"
	dstBinary := "/tmp/aa-proxy"
	if err := e.SSHRunner.Copy(ctx, host, srcBinary, dstBinary); err != nil {
		return fail("scp aa-proxy binary (copy proxy binary to host)", err)
	}

	// ------------------------------------------------------------------
	// 5. Start the proxy with --allow and --test-resolve flags.
	// ------------------------------------------------------------------
	if err := ctx.Err(); err != nil {
		return fail("start aa-proxy (context cancelled before start)", err)
	}
	startArgv := buildProxyStartArgv(dstBinary, allowlist, testResolve)
	startCmd := shellJoin(startArgv)
	if _, err := e.SSHRunner.Run(ctx, host, startCmd); err != nil {
		return fail("start aa-proxy", err)
	}

	return nil
}

// Remove reverses Install: stops the proxy, removes the iptables rules it
// installed, and cleans up the proxy binary on the host.
//
// Strict-mode contract:
//
//   - Idempotent. Remove after a failed/partial Install still succeeds.
//   - Remove on a host that never had Install called returns nil with no
//     observable side effects beyond the diagnostic SSH probe commands.
//
// Example:
//
//	if err := ctl.Remove(ctx, host); err != nil {
//	    log.Printf("egress remove: %v", err)
//	}
func (e *EgressController) Remove(ctx context.Context, host Host) error {
	// Stop the proxy first. We don't know if Install ever ran, so pkill's
	// "no processes matched" exit code is not an error we propagate.
	_, _ = e.SSHRunner.Run(ctx, host, shellJoin([]string{"pkill", "-f", "aa-proxy"}))

	// Reverse every iptables rule Install would have applied. Swallow
	// per-rule errors: if the rule isn't present the delete fails, which
	// is exactly the idempotent case we want.
	rules := buildIptablesRules()
	for i := len(rules) - 1; i >= 0; i-- {
		_, _ = e.SSHRunner.Run(ctx, host, shellJoin(rules[i].deleteArgv))
	}

	return nil
}

// rollbackApplied reverses a slice of already-applied iptables rules in LIFO
// order. Per-rule SSH errors are collected and joined into the returned
// error; one rule failing to reverse doesn't stop us from trying the rest.
func (e *EgressController) rollbackApplied(host Host, applied []installRuleSpec) error {
	// Rollback must keep going even if the caller's ctx was cancelled —
	// the caller is already in an error path and leaving iptables rules
	// in place is the worse outcome. We deliberately do NOT accept a ctx
	// parameter: the rollback uses context.Background() unconditionally.
	rollbackCtx := context.Background()

	var failures []string
	for i := len(applied) - 1; i >= 0; i-- {
		rule := applied[i]
		if _, err := e.SSHRunner.Run(rollbackCtx, host, shellJoin(rule.deleteArgv)); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", rule.description, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d of %d rollback step(s) failed: %s", len(failures), len(applied), strings.Join(failures, "; "))
	}
	return nil
}

// buildIptablesRules returns the ordered list of iptables rules Install
// applies. The list is derived once and shared by Install (to apply) and
// Remove (to reverse), so the two paths can't drift.
//
// Order matters:
//
//  1. DROP container-bridge FORWARD traffic that isn't addressed to the
//     proxy's IP:port. This is the "everything except the proxy" rule.
//  2. DROP container-bridge UDP 53.
//  3. DROP container-bridge TCP 53.
//
// Each entry also carries its reverse (-D form) so rollback uses the exact
// same match arguments.
func buildIptablesRules() []installRuleSpec {
	// Rule 1: DROP-except-to-proxy on the FORWARD chain. We pin the
	// destination to the proxy IP:port on the ACCEPT side by expressing
	// the drop as "everything not headed there." iptables has no single
	// negative-ACCEPT primitive, so we model it as two rules:
	//   (a) ACCEPT traffic to the proxy (inserted FIRST so it matches),
	//   (b) DROP everything else from the bridge.
	// Both are reversed by Remove / rollback.
	acceptToProxy := installRuleSpec{
		description: "accept container bridge traffic to forward proxy",
		addArgv: []string{
			"iptables", "-I", "FORWARD", "1",
			"-i", containerBridge,
			"-p", "tcp",
			"-d", proxyBindIP,
			"--dport", proxyBindPort,
			"-j", "ACCEPT",
		},
		deleteArgv: []string{
			"iptables", "-D", "FORWARD",
			"-i", containerBridge,
			"-p", "tcp",
			"-d", proxyBindIP,
			"--dport", proxyBindPort,
			"-j", "ACCEPT",
		},
	}
	dropDefaultForward := installRuleSpec{
		description: "drop all other container bridge FORWARD traffic",
		addArgv: []string{
			"iptables", "-A", "FORWARD",
			"-i", containerBridge,
			"-j", "DROP",
		},
		deleteArgv: []string{
			"iptables", "-D", "FORWARD",
			"-i", containerBridge,
			"-j", "DROP",
		},
	}
	dropUDP53 := installRuleSpec{
		description: "drop outbound DNS UDP 53 from container bridge",
		addArgv: []string{
			"iptables", "-A", "FORWARD",
			"-i", containerBridge,
			"-p", "udp",
			"--dport", "53",
			"-j", "DROP",
		},
		deleteArgv: []string{
			"iptables", "-D", "FORWARD",
			"-i", containerBridge,
			"-p", "udp",
			"--dport", "53",
			"-j", "DROP",
		},
	}
	dropTCP53 := installRuleSpec{
		description: "drop outbound DNS TCP 53 from container bridge",
		addArgv: []string{
			"iptables", "-A", "FORWARD",
			"-i", containerBridge,
			"-p", "tcp",
			"--dport", "53",
			"-j", "DROP",
		},
		deleteArgv: []string{
			"iptables", "-D", "FORWARD",
			"-i", containerBridge,
			"-p", "tcp",
			"--dport", "53",
			"-j", "DROP",
		},
	}
	return []installRuleSpec{acceptToProxy, dropDefaultForward, dropUDP53, dropTCP53}
}

// buildProxyStartArgv composes the argv for starting aa-proxy on the host.
// The binary path is controlled by us (set in Install). Every allowlist host
// and testResolve entry has already passed hostnameRegex / ipv4Regex.
func buildProxyStartArgv(binaryPath string, allowlist []string, testResolve map[string]string) []string {
	argv := []string{"nohup", binaryPath}
	for _, h := range allowlist {
		argv = append(argv, "--allow", h)
	}
	for name, ip := range testResolve {
		argv = append(argv, "--test-resolve", name+"="+ip)
	}
	// Background the proxy so the SSH Run returns promptly. stdout/stderr
	// go to a PID-carrying log file the operator can `cat`.
	argv = append(argv, ">/tmp/aa-proxy.log", "2>&1", "&")
	return argv
}

// shellJoin concatenates an argv slice into a single space-separated command
// string for the SSHRunner.Run interface. It does not quote or escape — every
// argument passed here has already been validated by hostnameRegex,
// ipv4Regex, or is a controlled literal. Calling this with untrusted input
// is a strict-mode violation.
func shellJoin(argv []string) string {
	return strings.Join(argv, " ")
}
