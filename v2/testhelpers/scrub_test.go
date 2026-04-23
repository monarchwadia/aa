package testhelpers

import (
	"strings"
	"testing"
)

// These meta-tests pin the scrubbing policy from ADR-2:
//   - Authorization header redacted -> "Bearer REDACTED"
//   - fo_[A-Za-z0-9]{16,} and FlyV1 [A-Za-z0-9_-]{20,} -> "REDACTED_TOKEN"
//   - org_slug JSON field -> "REDACTED_ORG"
//   - IPv4/IPv6 addresses in responses -> "REDACTED_IP"
// Each rule has a positive (redacts X) and a negative (leaves Y alone) test.
// The post-scrub forbidden-patterns check panics loudly if FLY_API_TOKEN /
// $USER / $HOME / token-shaped strings survived.

func TestScrub_AuthorizationHeaderRedacted(t *testing.T) {
	in := []byte(`{"headers":{"Authorization":"Bearer fo_abcdefghijklmnopqrstuvwx"}}`)
	out := scrub(in)
	if !strings.Contains(string(out), "Bearer REDACTED") {
		t.Fatalf("expected Authorization redaction, got: %s", out)
	}
	if strings.Contains(string(out), "fo_abcdefghijklmnopqrstuvwx") {
		t.Fatalf("token leaked post-scrub: %s", out)
	}
}

func TestScrub_NonSensitiveHeaderPreserved(t *testing.T) {
	in := []byte(`{"headers":{"Content-Type":"application/json"}}`)
	out := scrub(in)
	if !strings.Contains(string(out), "application/json") {
		t.Fatalf("non-sensitive header must not be redacted: %s", out)
	}
}

func TestScrub_FoPrefixedTokenRedacted(t *testing.T) {
	in := []byte(`{"token":"fo_abcdefghijklmnopqrstuv"}`)
	out := scrub(in)
	if strings.Contains(string(out), "fo_abcdefghijklmnopqrstuv") {
		t.Fatalf("fo_ token not redacted: %s", out)
	}
	if !strings.Contains(string(out), "REDACTED_TOKEN") {
		t.Fatalf("expected REDACTED_TOKEN marker, got: %s", out)
	}
}

func TestScrub_FlyV1TokenRedacted(t *testing.T) {
	in := []byte(`{"h":"FlyV1 abcdefghijklmnopqrstuvwx_yz"}`)
	out := scrub(in)
	if strings.Contains(string(out), "FlyV1 abcdefghijklmnopqrstuvwx_yz") {
		t.Fatalf("FlyV1 token not redacted: %s", out)
	}
}

func TestScrub_ShortTokenLikeStringPreserved(t *testing.T) {
	// too short to match either pattern — must survive
	in := []byte(`{"note":"fo_short","ref":"FlyV1 toobrief"}`)
	out := scrub(in)
	if !strings.Contains(string(out), "fo_short") {
		t.Fatalf("short fo_ string should not be redacted: %s", out)
	}
}

func TestScrub_OrgSlugRedacted(t *testing.T) {
	in := []byte(`{"org_slug":"monarch-personal"}`)
	out := scrub(in)
	if strings.Contains(string(out), "monarch-personal") {
		t.Fatalf("org_slug value leaked: %s", out)
	}
	if !strings.Contains(string(out), "REDACTED_ORG") {
		t.Fatalf("expected REDACTED_ORG marker, got: %s", out)
	}
}

func TestScrub_AppNamePreserved(t *testing.T) {
	// app slug is not sensitive — it is user-chosen and shows up in recordings
	in := []byte(`{"app_name":"my-public-app"}`)
	out := scrub(in)
	if !strings.Contains(string(out), "my-public-app") {
		t.Fatalf("app_name should be preserved: %s", out)
	}
}

func TestScrub_IPv4AddressRedacted(t *testing.T) {
	in := []byte(`{"ip":"192.168.1.42"}`)
	out := scrub(in)
	if strings.Contains(string(out), "192.168.1.42") {
		t.Fatalf("IPv4 address leaked: %s", out)
	}
	if !strings.Contains(string(out), "REDACTED_IP") {
		t.Fatalf("expected REDACTED_IP marker, got: %s", out)
	}
}

func TestScrub_IPv6AddressRedacted(t *testing.T) {
	in := []byte(`{"ip":"2606:4700:4700::1111"}`)
	out := scrub(in)
	if strings.Contains(string(out), "2606:4700:4700::1111") {
		t.Fatalf("IPv6 address leaked: %s", out)
	}
}

func TestScrub_VersionStringNotMistakenForIP(t *testing.T) {
	// 1.2.3 is not an IP; must not be mangled
	in := []byte(`{"version":"1.2.3"}`)
	out := scrub(in)
	if !strings.Contains(string(out), "1.2.3") {
		t.Fatalf("version string should not be treated as IP: %s", out)
	}
}

func TestPostScrubCheck_PanicsOnLeakedFlyAPIToken(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("post-scrub check should have panicked on leaked FLY_API_TOKEN literal")
		}
	}()
	leaked := []byte(`{"Authorization":"Bearer fo_supersecretliteralvalueABC"}`)
	postScrubCheck(leaked, postScrubContext{
		FlyAPITokenLiteral: "fo_supersecretliteralvalueABC",
		User:               "monarch",
		Home:               "/home/monarch",
	})
}

func TestPostScrubCheck_PanicsOnLeakedUser(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("post-scrub check should have panicked on leaked $USER")
		}
	}()
	leaked := []byte(`{"path":"/home/monarch/.config/aa"}`)
	postScrubCheck(leaked, postScrubContext{
		FlyAPITokenLiteral: "fo_unused_literal_xxxxxxxxxxxxxx",
		User:               "monarch",
		Home:               "/home/monarch",
	})
}

func TestPostScrubCheck_PanicsOnLeakedHome(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("post-scrub check should have panicked on leaked $HOME")
		}
	}()
	leaked := []byte(`{"path":"/Users/someoneelse/x"}`)
	postScrubCheck(leaked, postScrubContext{
		FlyAPITokenLiteral: "fo_unused_literal_xxxxxxxxxxxxxx",
		User:               "zzz",
		Home:               "/Users/someoneelse",
	})
}

func TestPostScrubCheck_PanicsOnSurvivingTokenShape(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("post-scrub check should have panicked on surviving token-shape")
		}
	}()
	// Shape matches fo_[A-Za-z0-9]{16,} — would be a regression in scrub
	leaked := []byte(`{"stray":"fo_aaaaaaaaaaaaaaaaa"}`)
	postScrubCheck(leaked, postScrubContext{
		FlyAPITokenLiteral: "fo_different_literal_xxxxxxxxxxxx",
		User:               "u",
		Home:               "/h",
	})
}

func TestPostScrubCheck_PassesOnCleanInput(t *testing.T) {
	clean := []byte(`{"a":"b","auth":"Bearer REDACTED","org":"REDACTED_ORG"}`)
	// Must not panic.
	postScrubCheck(clean, postScrubContext{
		FlyAPITokenLiteral: "fo_unused_literal_xxxxxxxxxxxxxx",
		User:               "alice",
		Home:               "/home/alice",
	})
}
