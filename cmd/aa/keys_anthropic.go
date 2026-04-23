package main

import (
	"context"
	"net/http"
)

// AnthropicKeyProvider implements EphemeralKeyProvider against the
// Anthropic Admin API. The laptop's long-lived admin key is held in
// AdminKey; each Mint call produces a short-lived, session-scoped API
// key that the agent uses for its run. Revoke destroys it.
//
// BaseURL points at the Admin API root (e.g. "https://api.anthropic.com").
// Tests override this to aim the client at an httptest fake. HTTPClient
// is optional; nil means http.DefaultClient.
//
// See docs/architecture/aa.md § "Workstreams" → ephemeral-key-anthropic,
// and README § "Credentials and ephemeral API keys".
type AnthropicKeyProvider struct {
	BaseURL    string
	AdminKey   string
	HTTPClient *http.Client
}

// NewAnthropicKeyProvider constructs a provider pointed at baseURL using
// adminKey to authenticate. HTTPClient is left nil; callers may set it
// after construction for custom transports or timeouts.
func NewAnthropicKeyProvider(baseURL, adminKey string) *AnthropicKeyProvider {
	return &AnthropicKeyProvider{
		BaseURL:  baseURL,
		AdminKey: adminKey,
	}
}

// Mint creates a fresh session-scoped key. Unimplemented: this file is
// the stub laid down by the ephemeral-key-anthropic workstream; the
// `implement` step turns these panics into a real Admin API client.
func (p *AnthropicKeyProvider) Mint(ctx context.Context, req MintRequest) (KeyHandle, string, error) {
	panic("unimplemented — see workstream ephemeral-key-anthropic in docs/architecture/aa.md")
}

// Revoke destroys the key identified by handle. Unimplemented: see Mint.
func (p *AnthropicKeyProvider) Revoke(ctx context.Context, handle KeyHandle) error {
	panic("unimplemented — see workstream ephemeral-key-anthropic in docs/architecture/aa.md")
}
