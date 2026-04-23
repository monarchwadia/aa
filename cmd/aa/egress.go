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
)

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
	panic("egress-controller workstream: EgressController.Install not yet implemented")
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
	panic("egress-controller workstream: EgressController.Remove not yet implemented")
}
