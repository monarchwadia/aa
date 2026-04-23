// Package registry — registry_test.go: HTTP client tests against httptest.Server.
//
// These are unit tests for the container-registry client in this package.
// A local httptest.Server stands in for registry.fly.io. We assert:
//   - List(prefix) filters repositories by tag prefix (ADR 2: default scoping).
//   - List(prefix="") returns everything reachable by the token (ls --all).
//   - Delete() issues the HTTP DELETE against the registry manifest endpoint.
//   - Every authenticated request carries a "Bearer <token>" Authorization header.
//
// No real network I/O. Bodies under test are stubs; this suite is red until Wave 2.
package registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer returns an httptest.Server plus a captured-request log the
// caller can read after the fact. Cleanup is registered on t.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var captured []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain and re-clone the body so the handler can still read it.
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		clone := r.Clone(r.Context())
		clone.Body = io.NopCloser(strings.NewReader(string(body)))
		captured = append(captured, clone)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// List returns every image when prefix is empty.
func TestRegistryListNoPrefixReturnsAll(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"repositories": []string{"aa-apps/myapi", "other-ns/foo"},
			"tags":         []string{"latest"},
		})
	})
	client := New(srv.URL, "tok")
	got, err := client.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected >=2 images across both namespaces, got %d", len(got))
	}
}

// List with a prefix filters to images whose tag starts with that prefix.
func TestRegistryListWithPrefixFilters(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"repositories": []string{"aa-apps/myapi", "other-ns/foo"},
			"tags":         []string{"latest"},
		})
	})
	client := New(srv.URL, "tok")
	got, err := client.List(context.Background(), "aa-apps/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, img := range got {
		if !strings.Contains(img.Tag, "aa-apps/") {
			t.Errorf("List returned image outside prefix: %q", img.Tag)
		}
	}
}

// List sends "Authorization: Bearer <token>" on every request.
func TestRegistryListSendsBearerAuth(t *testing.T) {
	srv, captured := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"repositories": []string{},
			"tags":         []string{},
		})
	})
	client := New(srv.URL, "my-token")
	_, err := client.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(*captured) == 0 {
		t.Fatal("no request captured")
	}
	for _, r := range *captured {
		got := r.Header.Get("Authorization")
		if got != "Bearer my-token" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer my-token")
		}
	}
}

// Delete issues an HTTP DELETE against the manifest endpoint for the tag.
func TestRegistryDeleteSendsHTTPDelete(t *testing.T) {
	srv, captured := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Method == http.MethodGet {
			w.Header().Set("Docker-Content-Digest", "sha256:deadbeef")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	client := New(srv.URL, "tok")
	err := client.Delete(context.Background(), "registry.fly.io/aa-apps/myapi:latest")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	foundDelete := false
	for _, r := range *captured {
		if r.Method == http.MethodDelete {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Fatal("no DELETE request observed")
	}
}

// Delete propagates a 401 into a typed auth error that names "token".
func TestRegistryDeleteAuthFailure(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	client := New(srv.URL, "bad")
	err := client.Delete(context.Background(), "registry.fly.io/aa-apps/myapi:latest")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "token") {
		t.Errorf("error should name token, got: %v", err)
	}
}

// Delete on a missing tag returns a 404-shaped error naming the tag.
func TestRegistryDeleteNotFound(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	client := New(srv.URL, "tok")
	err := client.Delete(context.Background(), "registry.fly.io/aa-apps/missing:latest")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the missing tag, got: %v", err)
	}
}

// Login is idempotent — calling it twice returns nil both times and does
// not re-contact the server for the second call (or at worst performs a
// cheap idempotent reauth). The invariant: no error, no panic.
func TestRegistryLoginIdempotent(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	client := New(srv.URL, "tok")
	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login #1: %v", err)
	}
	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login #2: %v", err)
	}
}
