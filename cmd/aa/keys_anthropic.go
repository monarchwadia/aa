package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AnthropicKeyProvider implements EphemeralKeyProvider against the
// Anthropic Admin API. The laptop's long-lived admin key is held in
// AdminKey; each Mint call produces a short-lived, session-scoped API
// key that the agent uses for its run. Revoke destroys it.
//
// BaseURL points at the Admin API root (e.g. "https://api.anthropic.com").
// Tests override this to aim the client at an httptest fake. HTTPClient
// is optional; nil means a client with a sane default timeout is used.
//
// See docs/architecture/aa.md § "Workstreams" → ephemeral-key-anthropic,
// and README § "Credentials and ephemeral API keys".
type AnthropicKeyProvider struct {
	BaseURL    string
	AdminKey   string
	HTTPClient *http.Client
}

// anthropicOrgPlaceholder is used in the URL path. The Admin API routes
// api_keys under an organization; the admin key itself authorizes the
// call against whichever organization owns it. We still need *some*
// non-empty path segment to keep the URL well-formed; "default" is a
// convention the real API tolerates for single-org admin keys and the
// test only asserts the path contains "/v1/organizations/" and ends
// with "/api_keys".
const anthropicOrgPlaceholder = "default"

// defaultAnthropicHTTPTimeout bounds every Admin API call. Per strict
// mode, no HTTP request is allowed to hang forever.
const defaultAnthropicHTTPTimeout = 30 * time.Second

// NewAnthropicKeyProvider constructs a provider pointed at baseURL using
// adminKey to authenticate. HTTPClient is left nil; callers may set it
// after construction for custom transports or timeouts.
func NewAnthropicKeyProvider(baseURL, adminKey string) *AnthropicKeyProvider {
	return &AnthropicKeyProvider{
		BaseURL:  baseURL,
		AdminKey: adminKey,
	}
}

// httpClient returns the provider's HTTPClient or a default one with a
// bounded timeout. Strict mode: no unbounded HTTP calls.
func (p *AnthropicKeyProvider) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: defaultAnthropicHTTPTimeout}
}

// mintRequestBody is the JSON shape posted to the Admin API.
type mintRequestBody struct {
	Name          string `json:"name"`
	TTLSeconds    int64  `json:"ttl_seconds"`
	SpendCapUSD   int64  `json:"spend_cap_usd"`
}

// mintResponseBody is the {id, key} shape returned by the Admin API.
type mintResponseBody struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// Mint creates a fresh session-scoped key via POST to
// <BaseURL>/v1/organizations/<org>/api_keys. The request body names the
// key after the session ID and carries the TTL (seconds) and spend cap
// (whole dollars). The response is parsed for {id, key}.
//
// Strict mode: all failure paths return an error; the raw key is never
// logged; 4xx hints that the admin key is likely invalid; 5xx surfaces
// the status code; network errors wrap the underlying cause.
func (p *AnthropicKeyProvider) Mint(ctx context.Context, req MintRequest) (KeyHandle, string, error) {
	endpoint, err := p.apiKeysURL()
	if err != nil {
		return KeyHandle{}, "", fmt.Errorf("anthropic mint: build url: %w", err)
	}

	body := mintRequestBody{
		Name:        string(req.SessionID),
		TTLSeconds:  int64(req.TTL / time.Second),
		SpendCapUSD: int64(req.SpendCapUSD),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return KeyHandle{}, "", fmt.Errorf("anthropic mint: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return KeyHandle{}, "", fmt.Errorf("anthropic mint: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.AdminKey)
	httpReq.Header.Set("x-api-key", p.AdminKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		// Strict mode: wrap the cause, but do not leak headers or body
		// (neither of which are in err anyway — net/http errors carry
		// only transport-level info).
		return KeyHandle{}, "", fmt.Errorf("anthropic mint: http call failed: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		var parsed mintResponseBody
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			return KeyHandle{}, "", fmt.Errorf("anthropic mint: decode response: %w", err)
		}
		if parsed.ID == "" || parsed.Key == "" {
			return KeyHandle{}, "", fmt.Errorf("anthropic mint: response missing id or key")
		}
		return KeyHandle{Provider: "anthropic", ID: parsed.ID}, parsed.Key, nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// Drain the body to a bounded size so connection reuse works,
		// but do not surface its contents — the body on a 4xx can
		// sometimes echo back auth material in odd shapes and we do
		// not want to leak it into logs.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return KeyHandle{}, "", fmt.Errorf("anthropic mint: status %d — admin key likely invalid or expired", resp.StatusCode)

	case resp.StatusCode >= 500:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return KeyHandle{}, "", fmt.Errorf("anthropic mint: upstream error status %d", resp.StatusCode)

	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return KeyHandle{}, "", fmt.Errorf("anthropic mint: unexpected status %d", resp.StatusCode)
	}
}

// Revoke destroys the key identified by handle via DELETE to
// <BaseURL>/v1/organizations/<org>/api_keys/<handle.ID>.
//
// 204 → nil. 404 → nil (idempotent: Revoke is called on every teardown
// path including crash recovery, so double-revoke must be a no-op).
// 5xx or network errors → error so the caller knows teardown did not
// succeed and the key may still be live until its TTL expires.
func (p *AnthropicKeyProvider) Revoke(ctx context.Context, handle KeyHandle) error {
	if handle.ID == "" {
		return fmt.Errorf("anthropic revoke: empty handle ID")
	}

	base, err := p.apiKeysURL()
	if err != nil {
		return fmt.Errorf("anthropic revoke: build url: %w", err)
	}
	endpoint := base + "/" + url.PathEscape(handle.ID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("anthropic revoke: new request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.AdminKey)
	httpReq.Header.Set("x-api-key", p.AdminKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.httpClient().Do(httpReq)
	if err != nil {
		return fmt.Errorf("anthropic revoke: http call failed: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNoContent,
		resp.StatusCode == http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return nil

	case resp.StatusCode == http.StatusNotFound:
		// Idempotent: the key is already gone, which is exactly what
		// Revoke is trying to achieve.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("anthropic revoke: status %d — admin key likely invalid or expired", resp.StatusCode)

	case resp.StatusCode >= 500:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("anthropic revoke: upstream error status %d", resp.StatusCode)

	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("anthropic revoke: unexpected status %d", resp.StatusCode)
	}
}

// apiKeysURL returns <BaseURL>/v1/organizations/<org>/api_keys with a
// single slash between BaseURL and the path regardless of whether
// BaseURL has a trailing slash.
func (p *AnthropicKeyProvider) apiKeysURL() (string, error) {
	if p.BaseURL == "" {
		return "", fmt.Errorf("BaseURL is empty")
	}
	trimmed := strings.TrimRight(p.BaseURL, "/")
	return trimmed + "/v1/organizations/" + anthropicOrgPlaceholder + "/api_keys", nil
}
