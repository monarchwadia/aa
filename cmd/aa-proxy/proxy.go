// Package main implements aa-proxy, a small HTTP CONNECT forward proxy.
//
// The proxy is the host-side component of aa's egress allowlist. Kernel
// iptables rules on the agent host drop every outbound packet from the
// container except those destined for the proxy; the proxy then enforces
// the hostname allowlist, returning 403 for any CONNECT to a host that is
// not explicitly allowed. Hostname resolution happens inside the proxy so
// a container cannot bypass the allowlist by, e.g., passing a raw IP or
// using curl --resolve.
//
// See docs/architecture/aa.md § "Decision 3" for the rationale behind
// shipping this in-tree instead of depending on tinyproxy.
package main

import "net"

// Proxy is an HTTP CONNECT forward proxy that enforces a hostname allowlist.
//
// A zero-value Proxy is not usable; construct with NewProxy. After
// construction, Handle serves a single accepted connection and Serve runs
// an accept loop against a listener.
type Proxy struct {
	// Allowlist is the set of exact hostnames the proxy will relay CONNECT
	// requests for. A single entry of "*" means unrestricted (accept any
	// hostname). Hostnames are matched case-insensitively against the host
	// portion of the CONNECT request line.
	Allowlist []string

	// TestResolve optionally overrides DNS resolution per hostname. When a
	// CONNECT request arrives for a host present in this map, the proxy
	// dials the mapped IP (host:port with the CONNECT-requested port)
	// instead of calling the system resolver. Intended for tests only;
	// production callers leave this nil.
	TestResolve map[string]string
}

// NewProxy constructs a Proxy with the given allowlist.
//
// The allowlist is used verbatim; callers are responsible for normalising
// it (e.g. lowercasing) before handing it in. A single-entry ["*"]
// allowlist disables host enforcement.
func NewProxy(allowlist []string) *Proxy {
	panic("unimplemented — see workstream `proxy-binary` in docs/architecture/aa.md § Workstreams")
}

// Handle serves a single CONNECT request on conn and then closes conn.
//
// Behaviour:
//   - On a parseable CONNECT whose host is in the allowlist, Handle
//     resolves the host (honouring TestResolve if set), dials the target,
//     writes "HTTP/1.1 200 Connection Established\r\n\r\n" to conn, and
//     splices bytes in both directions until either side closes.
//   - On a parseable CONNECT whose host is NOT in the allowlist, Handle
//     writes an HTTP 403 response and closes conn. No bytes are relayed.
//   - On an unparseable request line, Handle writes an HTTP 400 response
//     and closes conn.
//   - Any I/O error at any stage results in conn being closed. Handle
//     never panics on client or target disconnects.
func (p *Proxy) Handle(conn net.Conn) {
	panic("unimplemented — see workstream `proxy-binary` in docs/architecture/aa.md § Workstreams")
}

// Serve runs an accept loop on ln, spawning a goroutine per accepted
// connection that calls Handle. Serve returns when ln.Accept returns an
// error (typically because ln has been closed). The returned error is
// the terminal Accept error, or nil if the listener was cleanly closed.
func (p *Proxy) Serve(ln net.Listener) error {
	panic("unimplemented — see workstream `proxy-binary` in docs/architecture/aa.md § Workstreams")
}
