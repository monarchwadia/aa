package testhelpers

// Scrubbing policy for recorded snapshots (ADR-2). Generic rules redact
// Authorization bearer tokens, Fly token shapes, org_slug, and IP addresses.
// The post-scrub check greps for developer-specific literals and panics if
// any slipped through.

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// Authorization: Bearer ... — redact the whole value regardless of shape.
	reAuthorizationHeader = regexp.MustCompile(`"Authorization"\s*:\s*"Bearer [^"]*"`)
	// fo_ prefixed tokens, 16+ chars after the prefix.
	reFoToken = regexp.MustCompile(`fo_[A-Za-z0-9]{16,}`)
	// FlyV1 prefixed tokens, 20+ chars after the space.
	reFlyV1Token = regexp.MustCompile(`FlyV1 [A-Za-z0-9_-]{20,}`)
	// org_slug JSON field.
	reOrgSlug = regexp.MustCompile(`"org_slug"\s*:\s*"[^"]*"`)
	// IPv4 dotted quad. Word boundaries keep version strings like "1.2.3" safe.
	reIPv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// IPv6 — matches the common full and :: compressed forms. Requires at
	// least two colons plus hex groups so it won't snag plain "::" or "a:b".
	reIPv6 = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){2,7}(?::|[0-9A-Fa-f]{1,4})(?::[0-9A-Fa-f]{1,4})*\b`)
)

// scrub applies the ADR-2 generic redaction rules to serialized snapshot
// bytes (request or response, headers and body combined). The transformation
// is byte-in, byte-out and order-independent — rules do not depend on each
// other.
//
// Example:
//
//	out := scrub([]byte(`{"Authorization":"Bearer fo_abcdefghijklmnopqrst"}`))
//	// out contains "Bearer REDACTED"
func scrub(in []byte) []byte {
	out := in
	out = reAuthorizationHeader.ReplaceAll(out, []byte(`"Authorization":"Bearer REDACTED"`))
	out = reFoToken.ReplaceAll(out, []byte("REDACTED_TOKEN"))
	out = reFlyV1Token.ReplaceAll(out, []byte("REDACTED_TOKEN"))
	out = reOrgSlug.ReplaceAll(out, []byte(`"org_slug":"REDACTED_ORG"`))
	// IPv6 first so it doesn't get partially eaten by the IPv4 pass.
	out = reIPv6.ReplaceAll(out, []byte("REDACTED_IP"))
	out = reIPv4.ReplaceAll(out, []byte("REDACTED_IP"))
	return out
}

// postScrubContext holds developer-specific literal values the post-scrub
// forbidden-patterns grep must refuse to find in an about-to-be-committed
// snapshot.
type postScrubContext struct {
	FlyAPITokenLiteral string
	User               string
	Home               string
}

// postScrubCheck greps the post-scrub bytes for values that must never
// survive into a committed snapshot. Panics loudly on any hit, per ADR-2's
// "safety net" rule.
//
// Example:
//
//	postScrubCheck(serialized, postScrubContext{
//	    FlyAPITokenLiteral: os.Getenv("FLY_API_TOKEN"),
//	    User: os.Getenv("USER"),
//	    Home: os.Getenv("HOME"),
//	})
func postScrubCheck(bytesAfterScrub []byte, ctx postScrubContext) {
	s := string(bytesAfterScrub)
	if ctx.FlyAPITokenLiteral != "" && strings.Contains(s, ctx.FlyAPITokenLiteral) {
		panic(fmt.Sprintf("post-scrub leak: FLY_API_TOKEN literal survived into snapshot"))
	}
	if ctx.User != "" && strings.Contains(s, ctx.User) {
		panic(fmt.Sprintf("post-scrub leak: $USER %q survived into snapshot", ctx.User))
	}
	if ctx.Home != "" && strings.Contains(s, ctx.Home) {
		panic(fmt.Sprintf("post-scrub leak: $HOME %q survived into snapshot", ctx.Home))
	}
	if m := reFoToken.Find(bytesAfterScrub); m != nil {
		panic(fmt.Sprintf("post-scrub leak: fo_ token shape survived: %s", m))
	}
	if m := reFlyV1Token.Find(bytesAfterScrub); m != nil {
		panic(fmt.Sprintf("post-scrub leak: FlyV1 token shape survived: %s", m))
	}
}
