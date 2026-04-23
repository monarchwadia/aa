package testhelpers

// HTTP fakes: an httptest.Server per surface (api, registry). In replay mode
// the handler serves the next matching snapshot entry; in record mode (not
// yet exercised by meta-tests) it proxies to the real service, captures, and
// appends to the in-memory queue.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// httpFake owns one httptest.Server for a single surface discriminator
// ("api" or "registry"). It pulls entries off the sandbox's shared snapshot
// queue, matching only entries whose Surface equals its own.
type httpFake struct {
	surface string
	server  *httptest.Server
	queue   *snapshotQueue
	errs    *driftLog
}

// snapshotQueue is the shared, ordered list of remaining snapshot entries
// across both surfaces. The first entry whose Surface matches the incoming
// request's surface is consumed.
type snapshotQueue struct {
	mu      sync.Mutex
	entries []snapshotEntry
	// served counts total entries already handed out, used when reporting
	// "expected request #<n>" diagnostics.
	served int
}

func newSnapshotQueue(entries []snapshotEntry) *snapshotQueue {
	return &snapshotQueue{entries: append([]snapshotEntry(nil), entries...)}
}

func (q *snapshotQueue) popNextForSurface(surface string) (snapshotEntry, int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, e := range q.entries {
		if e.Surface == surface {
			q.entries = append(q.entries[:i], q.entries[i+1:]...)
			served := q.served
			q.served++
			return e, served, true
		}
	}
	return snapshotEntry{}, q.served, false
}

func (q *snapshotQueue) remaining() []snapshotEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]snapshotEntry(nil), q.entries...)
}

// driftLog collects mismatches and unexpected requests so the sandbox's
// cleanup hook can surface them via t.Errorf. Per ADR-3 the error shape is
// verbose and always-on.
type driftLog struct {
	mu   sync.Mutex
	msgs []string
}

func (d *driftLog) add(msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.msgs = append(d.msgs, msg)
}

func (d *driftLog) drain() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.msgs
	d.msgs = nil
	return out
}

// newReplayHTTPFake builds a fake for the given surface backed by the shared
// queue. The returned fake's server is already started; callers must close
// it via t.Cleanup.
func newReplayHTTPFake(surface string, queue *snapshotQueue, errs *driftLog) *httpFake {
	fake := &httpFake{surface: surface, queue: queue, errs: errs}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	return fake
}

func (f *httpFake) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	actual := recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Body:   body,
	}

	entry, served, ok := f.queue.popNextForSurface(f.surface)
	if !ok {
		f.errs.add(fmt.Sprintf(
			"unexpected request on surface %q (no remaining entries): %s %s",
			f.surface, actual.Method, actual.Path,
		))
		http.Error(w, "no remaining snapshot entries", 599)
		return
	}
	if diff := matchRequest(entry.Request, actual); diff != "" {
		f.errs.add(fmt.Sprintf(
			"request #%d on surface %q drift: %s\n  expected: %s %s\n  actual:   %s %s",
			served, f.surface, diff,
			entry.Request.Method, entry.Request.Path,
			actual.Method, actual.Path,
		))
		http.Error(w, "snapshot drift", 599)
		return
	}

	// Serve the recorded response.
	for k, v := range entry.Response.Headers {
		w.Header().Set(k, v)
	}
	status := entry.Response.Status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	if len(entry.Response.Body) > 0 {
		_, _ = io.Copy(w, bytes.NewReader(entry.Response.Body))
	}
}

func (f *httpFake) close() {
	if f.server != nil {
		f.server.Close()
	}
}

// url returns the base URL of the underlying httptest.Server.
func (f *httpFake) url() string {
	if f.server == nil {
		return ""
	}
	return f.server.URL
}
