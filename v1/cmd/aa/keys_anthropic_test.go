package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sessionIDForTest is a stable session ID used to name keys in the fake
// Admin API. Real `aa` derives this from repo+branch+timestamp; here we
// just need something unique enough to assert against.
const sessionIDForTest SessionID = "aa-demo-repo-feature-branch-20260423"

// newAnthropicFakeServer returns an httptest server whose handler is the
// provided function, and the cleanup is registered with t.
func newAnthropicFakeServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// TestAnthropicKeyProvider_MintHappyPath asserts that Mint hits the
// Admin API's POST /v1/organizations/.../api_keys endpoint with a body
// naming the session, parses {id, key} out of the response, and returns
// a KeyHandle plus the raw key string.
func TestAnthropicKeyProvider_MintHappyPath(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any

	srv := newAnthropicFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"apikey_01ABC","key":"sk-ant-fake-abc123"}`)
	})

	p := NewAnthropicKeyProvider(srv.URL, "fake-admin-key")

	handle, rawKey, err := p.Mint(context.Background(), MintRequest{
		SessionID:   sessionIDForTest,
		TTL:         8 * time.Hour,
		SpendCapUSD: 50,
	})
	if err != nil {
		t.Fatalf("Mint returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %q", gotMethod)
	}
	if !strings.Contains(gotPath, "/v1/organizations/") || !strings.HasSuffix(gotPath, "/api_keys") {
		t.Errorf("path %q does not look like /v1/organizations/.../api_keys", gotPath)
	}
	if handle.Provider != "anthropic" {
		t.Errorf("handle.Provider = %q, want %q", handle.Provider, "anthropic")
	}
	if handle.ID != "apikey_01ABC" {
		t.Errorf("handle.ID = %q, want %q", handle.ID, "apikey_01ABC")
	}
	if rawKey != "sk-ant-fake-abc123" {
		t.Errorf("rawKey = %q, want %q", rawKey, "sk-ant-fake-abc123")
	}
}

// TestAnthropicKeyProvider_MintSendsTTLAndSpendCap asserts the outgoing
// request body carries the TTL and spend cap at the expected JSON paths
// so the Admin API enforces them.
func TestAnthropicKeyProvider_MintSendsTTLAndSpendCap(t *testing.T) {
	var gotBody map[string]any

	srv := newAnthropicFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"apikey_01XYZ","key":"sk-ant-fake-xyz"}`)
	})

	p := NewAnthropicKeyProvider(srv.URL, "fake-admin-key")

	_, _, err := p.Mint(context.Background(), MintRequest{
		SessionID:   sessionIDForTest,
		TTL:         8 * time.Hour,
		SpendCapUSD: 50,
	})
	if err != nil {
		t.Fatalf("Mint returned error: %v", err)
	}

	// The body must mention the session name, a TTL, and a spend cap.
	// We don't pin the exact JSON shape — the workstream chooses — but
	// the values must be representable in the outgoing request.
	rawName, _ := json.Marshal(gotBody)
	body := string(rawName)
	if !strings.Contains(body, string(sessionIDForTest)) {
		t.Errorf("outgoing body did not include session ID %q: %s", sessionIDForTest, body)
	}
	if !strings.Contains(body, "28800") && !strings.Contains(body, "8h") {
		// 8h == 28800 seconds; accept either encoding
		t.Errorf("outgoing body did not include TTL (8h / 28800s): %s", body)
	}
	if !strings.Contains(body, "50") {
		t.Errorf("outgoing body did not include spend cap ($50): %s", body)
	}
}

// TestAnthropicKeyProvider_Mint5xxError asserts that a 5xx from the
// Admin API surfaces as an error including the status code, so
// operators can tell at a glance whether the provider is down.
func TestAnthropicKeyProvider_Mint5xxError(t *testing.T) {
	srv := newAnthropicFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"internal"}}`)
	})

	p := NewAnthropicKeyProvider(srv.URL, "fake-admin-key")

	_, _, err := p.Mint(context.Background(), MintRequest{
		SessionID:   sessionIDForTest,
		TTL:         8 * time.Hour,
		SpendCapUSD: 50,
	})
	if err == nil {
		t.Fatal("Mint on 500 returned nil error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error does not mention status 500: %v", err)
	}
}

// TestAnthropicKeyProvider_Mint4xxError asserts that a 401/403 from the
// Admin API surfaces as an error hinting that the admin key is bad.
// This is the most common operator-facing failure mode (rotated key,
// typo in env var, etc.) so the message needs to say so.
func TestAnthropicKeyProvider_Mint4xxError(t *testing.T) {
	srv := newAnthropicFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid x-api-key"}}`)
	})

	p := NewAnthropicKeyProvider(srv.URL, "fake-admin-key")

	_, _, err := p.Mint(context.Background(), MintRequest{
		SessionID:   sessionIDForTest,
		TTL:         8 * time.Hour,
		SpendCapUSD: 50,
	})
	if err == nil {
		t.Fatal("Mint on 401 returned nil error")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "admin") && !strings.Contains(msg, "key") {
		t.Errorf("401 error should hint at admin key being invalid/expired; got: %v", err)
	}
}

// TestAnthropicKeyProvider_MintNetworkError asserts that a dead server
// (connection refused) surfaces as an error with a cause. Exact text is
// provider-dependent; we just need err != nil and a non-empty message.
func TestAnthropicKeyProvider_MintNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // close before use so dial fails

	p := NewAnthropicKeyProvider(url, "fake-admin-key")

	_, _, err := p.Mint(context.Background(), MintRequest{
		SessionID:   sessionIDForTest,
		TTL:         8 * time.Hour,
		SpendCapUSD: 50,
	})
	if err == nil {
		t.Fatal("Mint against closed server returned nil error")
	}
	if err.Error() == "" {
		t.Error("Mint network error has empty message")
	}
}

// TestAnthropicKeyProvider_RevokeHappyPath asserts DELETE
// /v1/.../api_keys/<id> on a 204 response returns nil.
func TestAnthropicKeyProvider_RevokeHappyPath(t *testing.T) {
	var gotMethod, gotPath string

	srv := newAnthropicFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	p := NewAnthropicKeyProvider(srv.URL, "fake-admin-key")

	err := p.Revoke(context.Background(), KeyHandle{Provider: "anthropic", ID: "apikey_01ABC"})
	if err != nil {
		t.Fatalf("Revoke returned error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("expected DELETE, got %q", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/api_keys/apikey_01ABC") {
		t.Errorf("path %q does not end with /api_keys/apikey_01ABC", gotPath)
	}
}

// TestAnthropicKeyProvider_Revoke404Idempotent asserts that a 404
// (key already gone) is treated as success. Revoke is called on every
// teardown path including crash recovery, so double-revoke must be
// a no-op.
func TestAnthropicKeyProvider_Revoke404Idempotent(t *testing.T) {
	srv := newAnthropicFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
	})

	p := NewAnthropicKeyProvider(srv.URL, "fake-admin-key")

	err := p.Revoke(context.Background(), KeyHandle{Provider: "anthropic", ID: "apikey_gone"})
	if err != nil {
		t.Errorf("Revoke on 404 should be a no-op; got: %v", err)
	}
}

// TestAnthropicKeyProvider_Revoke5xxError asserts a 5xx surfaces as
// an error so the caller knows teardown didn't succeed and the key
// may still be live (TTL will eventually handle it).
func TestAnthropicKeyProvider_Revoke5xxError(t *testing.T) {
	srv := newAnthropicFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	p := NewAnthropicKeyProvider(srv.URL, "fake-admin-key")

	err := p.Revoke(context.Background(), KeyHandle{Provider: "anthropic", ID: "apikey_01ABC"})
	if err == nil {
		t.Fatal("Revoke on 502 returned nil error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error does not mention status 502: %v", err)
	}
}

// TestAnthropicKeyProvider_RevokeNetworkError asserts that a dead
// server surfaces as an error.
func TestAnthropicKeyProvider_RevokeNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	p := NewAnthropicKeyProvider(url, "fake-admin-key")

	err := p.Revoke(context.Background(), KeyHandle{Provider: "anthropic", ID: "apikey_01ABC"})
	if err == nil {
		t.Fatal("Revoke against closed server returned nil error")
	}
}
