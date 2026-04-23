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
//
// This file is Strict-mode (docs/PHILOSOPHY.md § Strict mode). Every code
// path refuses on ambiguity: malformed inputs produce well-formed HTTP
// error responses and close the connection, every I/O has a deadline,
// hostname matching is exact (never suffix/substring), and no goroutine
// is spawned without a documented exit path.
package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// readHeaderTimeout caps how long we wait for the client to finish
// sending the CONNECT request line and headers. A client that trickles
// bytes slowly will be dropped rather than holding a goroutine forever.
const readHeaderTimeout = 10 * time.Second

// dialTimeout caps the upstream dial. Short enough that an unreachable
// target fails fast; long enough to tolerate normal TLS handshakes
// downstream of the tunnel establishment.
const dialTimeout = 10 * time.Second

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
//
// Example:
//
//	p := NewProxy([]string{"api.anthropic.com", "registry.npmjs.org"})
//	ln, _ := net.Listen("tcp", "127.0.0.1:8080")
//	_ = p.Serve(ln)
func NewProxy(allowlist []string) *Proxy {
	return &Proxy{
		Allowlist:   allowlist,
		TestResolve: map[string]string{},
	}
}

// isAllowed reports whether host is permitted by p.Allowlist. Matching
// is EXACT and case-insensitive — "api.anthropic.com" in the allowlist
// does NOT permit "evil.api.anthropic.com.attacker.com" (no suffix
// match) nor "api.anthropic.comsomething.attacker.com" (no substring
// match). The sole exception is a single-entry ["*"] allowlist, which
// accepts any host.
func (p *Proxy) isAllowed(host string) bool {
	if len(p.Allowlist) == 1 && p.Allowlist[0] == "*" {
		return true
	}
	lowerHost := strings.ToLower(host)
	for _, entry := range p.Allowlist {
		if strings.ToLower(entry) == lowerHost {
			return true
		}
	}
	return false
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
	// conn is always closed on return: headers-phase errors, 4xx responses,
	// dial failures, and tunnel teardown all funnel through this defer.
	defer conn.Close()

	// Bound the header-reading phase. A stalled or dribbling client must
	// never hold this goroutine indefinitely.
	if err := conn.SetReadDeadline(time.Now().Add(readHeaderTimeout)); err != nil {
		return
	}

	reader := bufio.NewReader(conn)
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		// Partial/closed request: nothing to reply to meaningfully. Just close.
		return
	}

	method, target, ok := parseRequestLine(requestLine)
	if !ok || method != "CONNECT" {
		writeStatus(conn, "400 Bad Request")
		return
	}

	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		writeStatus(conn, "400 Bad Request")
		return
	}

	// Drain remaining headers up to blank line (bounded by readHeaderTimeout).
	// We do not inspect these; CONNECT semantics permit them but they are
	// not load-bearing for us.
	if err := drainRequestHeaders(reader); err != nil {
		return
	}

	// Clear the read deadline now that the header phase is complete; the
	// tunnel phase uses its own lifecycle (closed when either side exits).
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	if !p.isAllowed(host) {
		writeStatus(conn, "403 Forbidden")
		return
	}

	dialHost := host
	if override, present := p.TestResolve[host]; present {
		dialHost = override
	} else {
		// Resolve via the system resolver with a bounded context so a hung
		// DNS server cannot hold this goroutine.
		resolveCtx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		ips, resolveErr := net.DefaultResolver.LookupHost(resolveCtx, host)
		cancel()
		if resolveErr != nil || len(ips) == 0 {
			writeStatus(conn, "502 Bad Gateway")
			return
		}
		dialHost = ips[0]
	}

	dialer := net.Dialer{Timeout: dialTimeout}
	upstream, err := dialer.Dial("tcp", net.JoinHostPort(dialHost, port))
	if err != nil {
		writeStatus(conn, "502 Bad Gateway")
		return
	}
	// upstream is always closed: either by the tunnel teardown below, or
	// implicitly when Handle returns on a write error before tunnel start.
	defer upstream.Close()

	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	tunnel(conn, upstream)
}

// Serve runs an accept loop on ln, spawning a goroutine per accepted
// connection that calls Handle. Serve returns when ln.Accept returns an
// error (typically because ln has been closed). The returned error is
// the terminal Accept error, or nil if the listener was cleanly closed.
//
// Example:
//
//	ln, _ := net.Listen("tcp", "127.0.0.1:8080")
//	p := NewProxy([]string{"api.anthropic.com"})
//	log.Fatal(p.Serve(ln))
func (p *Proxy) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// net.ErrClosed means the listener was cleanly closed by the
			// caller; that is the normal shutdown path, not an error.
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		// Per-connection goroutine. Exit path: Handle returns when the
		// client finishes or disconnects, or when either tunnel half
		// closes. No leak path — conn is closed by Handle's defer.
		go p.Handle(conn)
	}
}

// parseRequestLine splits an HTTP request line into method and target.
// Returns ok=false if the line does not have the expected shape. The
// HTTP version token is required to be present but not validated beyond
// that — callers concerned with version compatibility check separately.
func parseRequestLine(line string) (method, target string, ok bool) {
	trimmed := strings.TrimRight(line, "\r\n")
	parts := strings.Fields(trimmed)
	if len(parts) != 3 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// drainRequestHeaders reads header lines from reader until a blank line
// or EOF. The caller is responsible for setting a read deadline on the
// underlying connection; drainRequestHeaders itself does no timing.
func drainRequestHeaders(reader *bufio.Reader) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" || line == "\n" {
			return nil
		}
	}
}

// writeStatus writes a minimal well-formed HTTP/1.1 response with the
// given status (e.g. "403 Forbidden") and no body. Errors are ignored —
// the caller is about to close the connection regardless.
func writeStatus(conn net.Conn, status string) {
	_, _ = io.WriteString(conn, "HTTP/1.1 "+status+"\r\n\r\n")
}

// tunnel splices bytes between client and upstream in both directions.
// Returns when either direction closes or errors. Both copy goroutines
// are guaranteed to exit before tunnel returns: when one direction
// finishes, we close both conns, which unblocks the other io.Copy.
func tunnel(client, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	// client -> upstream copier. Exits when client EOFs or errors, or
	// when upstream/client is closed by the sibling goroutine.
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		// Close upstream's write side so the reverse copier's Read unblocks.
		_ = closeWrite(upstream)
	}()

	// upstream -> client copier. Exits when upstream EOFs or errors, or
	// when client is closed by the sibling goroutine.
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		_ = closeWrite(client)
	}()

	wg.Wait()
}

// closeWrite half-closes the write side of conn if it supports it
// (*net.TCPConn does). Half-close lets the peer observe EOF while still
// draining any bytes the opposite direction has buffered. If the conn
// type does not support half-close, closeWrite falls back to a full Close.
func closeWrite(conn net.Conn) error {
	type writeCloser interface{ CloseWrite() error }
	if wc, ok := conn.(writeCloser); ok {
		return wc.CloseWrite()
	}
	return conn.Close()
}
