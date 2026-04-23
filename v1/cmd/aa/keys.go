package main

import (
	"context"
	"time"
)

// EphemeralKeyProvider mints and revokes short-lived, scoped API keys for an
// LLM provider. Each agent invocation that needs one uses its own fresh
// key; the key dies when the session is torn down (or when its TTL expires,
// whichever comes first).
//
// v1 ships one implementation: AnthropicKeyProvider. The interface is the
// seam for adding others (OpenAI, etc.) later without churning session
// orchestration code.
type EphemeralKeyProvider interface {
	// Mint creates a fresh session-scoped key with the given TTL and spend
	// cap, named for the session. Returns an opaque KeyHandle the caller
	// uses to revoke later, plus the raw key string to inject into the
	// agent's environment.
	Mint(ctx context.Context, req MintRequest) (KeyHandle, string, error)

	// Revoke destroys the key identified by handle. Safe to call twice
	// (second call is a no-op). Must be called on every session
	// termination path (push, kill, crash-recovery).
	Revoke(ctx context.Context, handle KeyHandle) error
}

// MintRequest is the parameters for minting a new ephemeral key.
type MintRequest struct {
	// SessionID is used to name the key so `aa sweep` can find orphans.
	SessionID SessionID

	// TTL is how long the key stays valid before the provider expires it
	// regardless of revocation. Belt-and-suspenders against a laptop that
	// never cleans up. Default 8h per intent.
	TTL time.Duration

	// SpendCapUSD bounds the damage if the agent goes haywire. Default $50
	// per intent; 0 means no cap (not recommended).
	SpendCapUSD float64
}

// KeyHandle is the opaque identifier for a minted key. Structure is
// provider-specific; callers treat it as a handle.
type KeyHandle struct {
	// Provider is the short name of the ephemeral-key provider, e.g.
	// "anthropic". Used to dispatch Revoke to the right implementation.
	Provider string

	// ID is the provider-assigned identifier of the key (for Anthropic,
	// the `id` field of the Admin API's POST /api_keys response).
	ID string
}
