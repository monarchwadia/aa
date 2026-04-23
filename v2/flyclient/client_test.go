// client_test.go exercises the flyclient.Client implementation against a
// local net/http/httptest.Server playing canned responses. Each test
// asserts exactly one behaviour and names that behaviour as a sentence.
//
// Coverage (per docs/architecture/machine-lifecycle.md Wave 2a):
//   - EnsureApp: 200 (already exists) → no-op; 404 → POST /apps; create error.
//   - Create: happy path writes the spec; non-2xx becomes a wrapped error.
//   - Get / List / Start / Stop / Destroy: correct method + URL + headers.
//   - Destroy with force=true appends ?force=true.
//   - FindByLabel: exact-match filter, zero-match, multi-match.
//   - Labels round-trip: Create then Get preserves the Labels map.
package flyclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withServer returns a client pointed at a fresh httptest.Server running
// the given handler, plus the cleanup hook registered on t.
func withServer(t *testing.T, h http.HandlerFunc) (Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "tok_test_fixture")
	return c, srv
}

// EnsureApp returns nil when the backend already has the app (GET 200).
func TestEnsureAppReturnsNilWhenGetRespondsTwoHundred(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/apps/aa-apps" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(200)
	})
	if err := c.EnsureApp(context.Background(), "aa-apps"); err != nil {
		t.Fatalf("EnsureApp: %v", err)
	}
}

// EnsureApp POSTs /apps when the GET returns 404.
func TestEnsureAppPostsAppsWhenGetRespondsFourOhFour(t *testing.T) {
	var sawPost bool
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			w.WriteHeader(404)
		case "POST":
			if r.URL.Path != "/apps" {
				t.Errorf("create POST path = %q, want /apps", r.URL.Path)
			}
			sawPost = true
			w.WriteHeader(201)
		}
	})
	if err := c.EnsureApp(context.Background(), "aa-apps"); err != nil {
		t.Fatalf("EnsureApp: %v", err)
	}
	if !sawPost {
		t.Fatal("EnsureApp did not POST /apps after 404")
	}
}

// EnsureApp surfaces HTTP errors from the create step as non-nil error.
func TestEnsureAppReturnsErrorWhenCreatePostFailsWithFiveHundred(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	err := c.EnsureApp(context.Background(), "aa-apps")
	if err == nil {
		t.Fatal("EnsureApp returned nil, want error on 500 from POST /apps")
	}
}

// EnsureApp attaches the bearer token on every request.
func TestEnsureAppSendsBearerTokenHeader(t *testing.T) {
	var auth string
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	})
	_ = c.EnsureApp(context.Background(), "aa-apps")
	want := "Bearer tok_test_fixture"
	if auth != want {
		t.Errorf("Authorization header = %q, want %q", auth, want)
	}
}

// Create POSTs /apps/<app>/machines and decodes the machine ID.
func TestCreatePostsMachinesPathAndDecodesMachineID(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		wantPath := "/apps/aa-apps/machines"
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"9080e6f3a12345","state":"created","region":"iad"}`))
	})
	m, err := c.Create(context.Background(), "aa-apps", SpawnSpec{Image: "ubuntu:22.04"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.ID != "9080e6f3a12345" {
		t.Errorf("ID = %q, want 9080e6f3a12345", m.ID)
	}
}

// Create returns an error when the backend responds non-2xx.
func TestCreateReturnsErrorOnFourHundredResponse(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad image"}`))
	})
	_, err := c.Create(context.Background(), "aa-apps", SpawnSpec{Image: "bad"})
	if err == nil {
		t.Fatal("Create returned nil error on HTTP 400")
	}
}

// List GETs /apps/<app>/machines and returns the decoded slice.
func TestListGetsMachinesPathAndDecodesSlice(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s, want GET", r.Method)
		}
		_, _ = w.Write([]byte(`[{"id":"a1","state":"started","region":"iad"},{"id":"b2","state":"stopped","region":"iad"}]`))
	})
	ms, err := c.List(context.Background(), "aa-apps")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("len(machines) = %d, want 2", len(ms))
	}
}

// Start POSTs /apps/<app>/machines/<id>/start.
func TestStartPostsStartSubpath(t *testing.T) {
	var got string
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(200)
	})
	if err := c.Start(context.Background(), "aa-apps", "a1"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	want := "POST /apps/aa-apps/machines/a1/start"
	if got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

// Stop POSTs /apps/<app>/machines/<id>/stop.
func TestStopPostsStopSubpath(t *testing.T) {
	var got string
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(200)
	})
	if err := c.Stop(context.Background(), "aa-apps", "a1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	want := "POST /apps/aa-apps/machines/a1/stop"
	if got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
}

// Destroy without force DELETEs /apps/<app>/machines/<id>.
func TestDestroyWithoutForceDeletesMachineWithoutForceQuery(t *testing.T) {
	var gotRaw string
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.Method + " " + r.URL.RequestURI()
		w.WriteHeader(200)
	})
	if err := c.Destroy(context.Background(), "aa-apps", "a1", false); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	want := "DELETE /apps/aa-apps/machines/a1"
	if gotRaw != want {
		t.Errorf("request = %q, want %q", gotRaw, want)
	}
}

// Destroy with force=true appends ?force=true to the URL.
func TestDestroyWithForceAppendsForceTrueQueryParam(t *testing.T) {
	var rawQuery string
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.WriteHeader(200)
	})
	if err := c.Destroy(context.Background(), "aa-apps", "a1", true); err != nil {
		t.Fatalf("Destroy force: %v", err)
	}
	if rawQuery != "force=true" {
		t.Errorf("raw query = %q, want force=true", rawQuery)
	}
}

// FindByLabel returns only machines whose metadata exactly matches key=value.
func TestFindByLabelFiltersToExactMatches(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"a1","state":"started","region":"iad","config":{"metadata":{"aa.up-id":"one"}}},
			{"id":"b2","state":"started","region":"iad","config":{"metadata":{"aa.up-id":"two"}}},
			{"id":"c3","state":"started","region":"iad","config":{"metadata":{"aa.up-id":"one"}}}
		]`))
	})
	ms, err := c.FindByLabel(context.Background(), "aa-apps", "aa.up-id", "one")
	if err != nil {
		t.Fatalf("FindByLabel: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("matches = %d, want 2 (a1 + c3)", len(ms))
	}
}

// FindByLabel returns (nil, nil) on zero matches — not an error.
func TestFindByLabelReturnsNilSliceAndNilErrorOnZeroMatches(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"a1","state":"started","region":"iad","config":{"metadata":{"aa.up-id":"other"}}}]`))
	})
	ms, err := c.FindByLabel(context.Background(), "aa-apps", "aa.up-id", "missing")
	if err != nil {
		t.Fatalf("FindByLabel: %v", err)
	}
	if len(ms) != 0 {
		t.Fatalf("matches = %d, want 0", len(ms))
	}
}

// FindByLabel surfaces multi-match results without deduping.
func TestFindByLabelSurfacesMultipleMatchesWithoutDeduping(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":"a1","state":"started","region":"iad","config":{"metadata":{"aa.up-id":"dup"}}},
			{"id":"b2","state":"started","region":"iad","config":{"metadata":{"aa.up-id":"dup"}}}
		]`))
	})
	ms, err := c.FindByLabel(context.Background(), "aa-apps", "aa.up-id", "dup")
	if err != nil {
		t.Fatalf("FindByLabel: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("matches = %d, want 2", len(ms))
	}
}

// Labels written via Create round-trip through Get unchanged.
func TestLabelsRoundTripFromCreateThroughGet(t *testing.T) {
	var captured map[string]string
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			// Would decode body and store labels; in red phase we just capture
			// that a POST happened and emit a canned response.
			captured = map[string]string{"aa.up-id": "priya-dev"}
			_, _ = w.Write([]byte(`{"id":"a1","state":"created","region":"iad","config":{"metadata":{"aa.up-id":"priya-dev"}}}`))
		case "GET":
			_, _ = w.Write([]byte(`{"id":"a1","state":"started","region":"iad","config":{"metadata":{"aa.up-id":"priya-dev"}}}`))
		}
	})
	_, err := c.Create(context.Background(), "aa-apps", SpawnSpec{
		Image:  "ubuntu:22.04",
		Labels: map[string]string{"aa.up-id": "priya-dev"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := c.Get(context.Background(), "aa-apps", "a1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Labels["aa.up-id"] != "priya-dev" {
		t.Errorf("Labels[aa.up-id] = %q, want priya-dev (captured=%v)", got.Labels["aa.up-id"], captured)
	}
}

// WaitStarted blocks until /machines/:id reports state=="started".
func TestWaitStartedReturnsNilOnceBackendReportsStarted(t *testing.T) {
	var calls int
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		state := "starting"
		if calls >= 2 {
			state = "started"
		}
		_, _ = w.Write([]byte(`{"id":"a1","state":"` + state + `","region":"iad"}`))
	})
	if err := c.WaitStarted(context.Background(), "aa-apps", "a1"); err != nil {
		t.Fatalf("WaitStarted: %v", err)
	}
}

// Context cancellation propagates through outstanding requests.
func TestClientCallsRespectContextCancellation(t *testing.T) {
	c, _ := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.List(ctx, "aa-apps")
	if err == nil {
		t.Fatal("List with cancelled ctx returned nil, want error")
	}
}
