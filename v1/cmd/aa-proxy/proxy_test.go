package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// startProxy binds a Proxy to 127.0.0.1:0, runs its accept loop in a
// goroutine, and returns the listener address and a stop function. Tests
// that want to exercise the accept loop call this helper; tests that want
// to invoke Handle directly on a single connection can bypass it.
func startProxy(t *testing.T, p *Proxy) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.Serve(ln)
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not return after listener close")
		}
	}
}

// hostPort splits the "host:port" from an httptest server URL.
func hostPort(t *testing.T, server *httptest.Server) (host, port, hostPort string) {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse httptest url %q: %v", server.URL, err)
	}
	h, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host/port %q: %v", u.Host, err)
	}
	return h, p, u.Host
}

// sendConnect writes a CONNECT request line and reads back the full status
// line from the proxy.
func sendConnect(t *testing.T, conn net.Conn, target string) (statusLine string, r *bufio.Reader) {
	t.Helper()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	r = bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT response line: %v", err)
	}
	return strings.TrimRight(line, "\r\n"), r
}

// drainHeaders reads the rest of the response headers up to the blank
// line. Used after reading the status line so subsequent bytes on the
// tunnel can be read cleanly.
func drainHeaders(t *testing.T, r *bufio.Reader) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read header line: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			return
		}
	}
}

// TestConnectAllowlistedHost verifies that CONNECT to an allowlisted host
// returns 200 and bytes flow through the tunnel.
func TestConnectAllowlistedHost(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello from target")
	}))
	defer target.Close()

	host, _, hp := hostPort(t, target)
	p := NewProxy([]string{host})
	proxyAddr, stop := startProxy(t, p)
	defer stop()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	status, r := sendConnect(t, conn, hp)
	if !strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Fatalf("expected 200 status, got %q", status)
	}
	drainHeaders(t, r)

	// Send an HTTP GET over the tunnel.
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", hp); err != nil {
		t.Fatalf("write tunneled GET: %v", err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	if !strings.Contains(string(body), "hello from target") {
		t.Fatalf("expected target body in response, got %q", string(body))
	}
}

// TestConnectNonAllowlistedHost verifies the proxy returns 403 and closes.
//
// Uses distinct hostnames so the allowlist check actually matters:
// httptest.NewServer always binds to 127.0.0.1, so putting the raw server
// host in the allowlist would also admit any OTHER httptest server.
// Instead we allowlist "allowed.example" (routed to the running server via
// TestResolve) and CONNECT to "blocked.example" which has no TestResolve
// entry and is not in the allowlist — exactly the exfil-attempt shape.
func TestConnectNonAllowlistedHost(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("blocked target received a request; proxy should have rejected the CONNECT")
	}))
	defer blocked.Close()

	_, blockedPort, _ := hostPort(t, blocked)

	p := NewProxy([]string{"allowed.example"})
	proxyAddr, stop := startProxy(t, p)
	defer stop()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	status, _ := sendConnect(t, conn, "blocked.example:"+blockedPort)
	if !strings.HasPrefix(status, "HTTP/1.1 403") {
		t.Fatalf("expected 403 status, got %q", status)
	}

	// After 403 the proxy must close; the next read returns EOF (possibly
	// after some body bytes, which we drain).
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
}

// TestUnparseableRequestLine verifies that a malformed request yields 400.
func TestUnparseableRequestLine(t *testing.T) {
	p := NewProxy([]string{"example.com"})
	proxyAddr, stop := startProxy(t, p)
	defer stop()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GARBAGE NOT A REQUEST LINE\r\n\r\n")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read response line: %v", err)
	}
	if !strings.HasPrefix(line, "HTTP/1.1 400") {
		t.Fatalf("expected 400 status, got %q", strings.TrimRight(line, "\r\n"))
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
}

// TestTestResolveOverride verifies TestResolve steers the proxy at a
// different IP than the hostname would normally resolve to. We point a
// made-up allowlisted hostname at the real httptest server's address and
// confirm traffic reaches the server.
func TestTestResolveOverride(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "resolved-via-test-map")
	}))
	defer target.Close()

	_, port, _ := hostPort(t, target)
	fake := "made-up.internal"
	fakeHP := net.JoinHostPort(fake, port)

	p := NewProxy([]string{fake})
	// Map the made-up hostname to the real httptest server's IP.
	targetHost, _, _ := hostPort(t, target)
	p.TestResolve = map[string]string{fake: targetHost}

	proxyAddr, stop := startProxy(t, p)
	defer stop()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	status, r := sendConnect(t, conn, fakeHP)
	if !strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Fatalf("expected 200 status, got %q", status)
	}
	drainHeaders(t, r)

	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", fakeHP); err != nil {
		t.Fatalf("write tunneled GET: %v", err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	if !strings.Contains(string(body), "resolved-via-test-map") {
		t.Fatalf("expected TestResolve to route to httptest server, got %q", string(body))
	}
}

// TestAllowlistWildcard verifies ["*"] accepts any CONNECT.
func TestAllowlistWildcard(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "wildcard ok")
	}))
	defer target.Close()

	_, _, hp := hostPort(t, target)
	p := NewProxy([]string{"*"})
	proxyAddr, stop := startProxy(t, p)
	defer stop()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	status, _ := sendConnect(t, conn, hp)
	if !strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Fatalf("expected 200 under wildcard allowlist, got %q", status)
	}
}

// TestConcurrentConnects runs 5 CONNECTs in parallel; all must succeed.
func TestConcurrentConnects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()

	host, _, hp := hostPort(t, target)
	p := NewProxy([]string{host})
	proxyAddr, stop := startProxy(t, p)
	defer stop()

	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := net.Dial("tcp", proxyAddr)
			if err != nil {
				errs <- fmt.Errorf("worker %d dial: %v", i, err)
				return
			}
			defer conn.Close()
			status, r := sendConnect(t, conn, hp)
			if !strings.HasPrefix(status, "HTTP/1.1 200") {
				errs <- fmt.Errorf("worker %d expected 200, got %q", i, status)
				return
			}
			drainHeaders(t, r)
			if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", hp); err != nil {
				errs <- fmt.Errorf("worker %d tunnel write: %v", i, err)
				return
			}
			body, err := io.ReadAll(r)
			if err != nil {
				errs <- fmt.Errorf("worker %d tunnel read: %v", i, err)
				return
			}
			if !strings.Contains(string(body), "ok") {
				errs <- fmt.Errorf("worker %d missing body, got %q", i, string(body))
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestClientDisconnectsMidConnect verifies the proxy doesn't panic when
// the client hangs up before finishing the CONNECT request.
func TestClientDisconnectsMidConnect(t *testing.T) {
	p := NewProxy([]string{"example.com"})
	proxyAddr, stop := startProxy(t, p)
	defer stop()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	// Write a partial request line then slam the connection shut.
	_, _ = conn.Write([]byte("CONNECT exam"))
	_ = conn.Close()

	// Give the server a moment to observe the close; if it panics the
	// test binary will crash.
	time.Sleep(200 * time.Millisecond)
}

// TestTargetDisconnectsMidTunnel verifies the proxy doesn't panic when the
// upstream target closes while a tunnel is live.
func TestTargetDisconnectsMidTunnel(t *testing.T) {
	// A TCP server that accepts one connection, reads a little, then
	// closes. Simulates an upstream that hangs up mid-tunnel.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		c, err := targetLn.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 16)
		_, _ = c.Read(buf)
		_ = c.Close()
	}()

	tAddr := targetLn.Addr().(*net.TCPAddr)
	host := tAddr.IP.String()
	port := fmt.Sprintf("%d", tAddr.Port)
	hp := net.JoinHostPort(host, port)

	p := NewProxy([]string{host})
	proxyAddr, stop := startProxy(t, p)
	defer stop()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()

	status, r := sendConnect(t, conn, hp)
	if !strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Fatalf("expected 200 status, got %q", status)
	}
	drainHeaders(t, r)

	// Push a few bytes so the server's Read returns and it closes.
	_, _ = conn.Write([]byte("hello"))

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
	<-serverDone
}
