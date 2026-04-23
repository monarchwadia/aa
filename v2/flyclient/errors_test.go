// errors_test.go pins the sentinel-error contract of flyclient: HTTP
// 404 responses map to a sentinel callers can pattern-match on; HTTP
// 409 maps to a conflict sentinel; 5xx responses wrap the body text so
// diagnostic output ends up in the user-facing error path (philosophy
// axis 3). Expected RED until Wave 2 lands the sentinel types.
package flyclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A 404 on Get returns an error that matches flyclient.ErrNotFound.
func TestGetNotFoundReturnsErrorMatchingErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "tok")
	_, err := c.Get(context.Background(), "aa-apps", "missing")
	if err == nil {
		t.Fatal("Get on 404: err = nil, want ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get on 404: err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

// A 409 on Create returns an error that matches flyclient.ErrConflict.
func TestCreateConflictReturnsErrorMatchingErrConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"already exists"}`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "tok")
	_, err := c.Create(context.Background(), "aa-apps", SpawnSpec{Image: "ubuntu:22.04"})
	if err == nil {
		t.Fatal("Create on 409: err = nil, want ErrConflict")
	}
	if !errors.Is(err, ErrConflict) {
		t.Errorf("Create on 409: err = %v, want errors.Is(err, ErrConflict)", err)
	}
}

// A 500 with a JSON body surfaces that body in the error string.
func TestFiveHundredErrorIncludesResponseBodyInMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`boom-diagnostic-text`))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "tok")
	_, err := c.List(context.Background(), "aa-apps")
	if err == nil {
		t.Fatal("List on 500: err = nil, want wrapped error")
	}
	if !contains(err.Error(), "boom-diagnostic-text") {
		t.Errorf("err = %q, must include server diagnostic text", err.Error())
	}
}

// contains is the stdlib-only substring check; avoids pulling in strings here
// so this file stays tight to the sentinel-error contract.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
