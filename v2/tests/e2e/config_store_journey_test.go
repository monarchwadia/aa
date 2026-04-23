// Package e2e contains end-to-end user-journey tests for the aa CLI.
//
// PERSONA
//   The solo operator. Runs aa many times a day from their own laptop.
//   Currently pastes or re-exports the Fly.io token on every command, losing
//   time and leaking the value into shell history. They reach for `aa config`
//   first because the user-facing docs promise a "set once, never paste
//   again" flow, and they want to confirm the tool actually delivers on that
//   promise before they trust it with anything important. Adapted from the
//   primary persona in v2/docs/intent/config-store.md.
//
// JOURNEY
//   1. Operator runs `aa config token.flyio=fo1_...` from a fresh sandbox.
//      WHY: they want to stop pasting the token into every invocation and
//      out of shell history; this is the single step that makes the whole
//      "set once" promise real.
//      OBSERVES: exit 0; stdout contains the literal line `saved token.flyio`;
//      the config file at ConfigPath() exists and contains the token value.
//
//   2. Operator runs `aa config defaults.app=my-team-app endpoints.api=https://api.staging.fly.io/v1`.
//      WHY: they want to pin a non-secret default and point the CLI at a
//      staging endpoint, in one invocation, through the same command surface
//      the docs promised would absorb future settings with no new concepts.
//      OBSERVES: exit 0; stdout contains `saved defaults.app` and
//      `saved endpoints.api` (one line per key, input order); the config
//      file now holds all three keys.
//
//   3. Operator runs `aa config` (list) on the sandbox from step 2.
//      WHY: they want to ask the tool what it knows about them without
//      grepping hidden state, and confirm the secret is not re-leaked to
//      the screen.
//      OBSERVES: exit 0; three lines on stdout; the `token.flyio` line is
//      masked as `token.flyio=<set>` (per architecture ADR 2); the
//      non-secret lines render literally as `defaults.app=my-team-app`
//      and `endpoints.api=https://api.staging.fly.io/v1`.
//
//   4. Operator runs `aa config --remove endpoints.api`, then `aa config`.
//      WHY: they want to drop a stale value cleanly — the "rotating or
//      revoking a credential" sub-persona from intent. After removal, the
//      key must no longer show up in the listing.
//      OBSERVES: remove exits 0 and prints `removed endpoints.api`; the
//      follow-up list no longer contains any `endpoints.api` line.
//
//   5. Operator runs `aa config` in a fresh, empty sandbox.
//      WHY: they came back after a break and want to verify whether the
//      tool has anything stored at all; the empty-state answer must be
//      unambiguous and stable enough to script against.
//      OBSERVES: exit 0; stdout is the literal line `(no config set)`.
//
//   6. Operator runs `aa machine ls` in a fresh sandbox with no token set.
//      WHY: they expect the tool to fail loud and tell them exactly which
//      key to set — not silently hang, not crash with a stack trace, not
//      say "invalid config". This is the "errors include what and
//      what-next" promise from PHILOSOPHY.md axis 3.
//      OBSERVES: non-zero exit; stderr names `token.flyio` and contains
//      the literal remediation command `aa config token.flyio=`.
//
// BUSINESS IMPACT IF BROKEN
//   If step 1 breaks, every new user hits it on their first real use of aa
//   and cannot proceed — the tool's "set once" differentiator dies on
//   contact. If step 3's masking breaks, a routine `aa config` during a
//   pair-programming screenshare leaks the Fly.io token to anyone watching,
//   eroding trust in the whole store. If step 5's empty-state string drifts,
//   every script parsing `aa config` output silently misbehaves on fresh
//   machines. If step 6's error message stops naming `token.flyio`, the
//   returning user is stuck with a generic failure and has to re-read the
//   docs to recover — the exact ceremony the config store was built to
//   eliminate. Together these steps are the product's onboarding contract;
//   any one of them regressing makes aa feel broken on day one.
package e2e

import (
	"os"
	"strings"
	"testing"

	"aa/v2/testhelpers"
)

func TestConfigStoreJourney(t *testing.T) {
	// Step 1: Set the Fly.io token from a fresh sandbox. This is the
	// foundational "set once" move; everything downstream assumes it works.
	t.Run("step1_set_token_from_fresh_sandbox", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "config_store_step1_set_token")

		const tokenValue = "fo1_abc123yourrealtokenhere"
		result := sandbox.RunAA(t, []string{"config", "token.flyio=" + tokenValue}, nil)

		if result.ExitCode != 0 {
			t.Fatalf("step 1: expected exit 0 setting token, got %d; stderr=%q", result.ExitCode, result.Stderr)
		}
		if !strings.Contains(result.Stdout, "saved token.flyio") {
			t.Fatalf("step 1: expected stdout to contain %q, got %q", "saved token.flyio", result.Stdout)
		}

		configPath := sandbox.ConfigPath()
		bytes, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("step 1: expected config file at %s to be readable after set, got error: %v", configPath, err)
		}
		if !strings.Contains(string(bytes), tokenValue) {
			t.Fatalf("step 1: expected config file at %s to contain the token value %q, got %q", configPath, tokenValue, string(bytes))
		}
	})

	// Step 2: Set two more keys in one invocation — one reserved-prefix
	// default, one endpoints key from the architecture Amendments section.
	// Verifies the multi-arg set path and the three-key final state.
	t.Run("step2_set_multiple_keys_in_one_invocation", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "config_store_step2_set_multi")

		// Preload the token first so the final file has all three keys.
		setToken := sandbox.RunAA(t, []string{"config", "token.flyio=fo1_abc123yourrealtokenhere"}, nil)
		if setToken.ExitCode != 0 {
			t.Fatalf("step 2 precondition: expected exit 0 seeding token, got %d; stderr=%q", setToken.ExitCode, setToken.Stderr)
		}

		result := sandbox.RunAA(t, []string{
			"config",
			"defaults.app=my-team-app",
			"endpoints.api=https://api.staging.fly.io/v1",
		}, nil)

		if result.ExitCode != 0 {
			t.Fatalf("step 2: expected exit 0 setting two keys, got %d; stderr=%q", result.ExitCode, result.Stderr)
		}
		if !strings.Contains(result.Stdout, "saved defaults.app") {
			t.Fatalf("step 2: expected stdout to contain %q, got %q", "saved defaults.app", result.Stdout)
		}
		if !strings.Contains(result.Stdout, "saved endpoints.api") {
			t.Fatalf("step 2: expected stdout to contain %q, got %q", "saved endpoints.api", result.Stdout)
		}

		configPath := sandbox.ConfigPath()
		bytes, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("step 2: expected config file at %s to be readable, got error: %v", configPath, err)
		}
		contents := string(bytes)
		for _, key := range []string{"token.flyio", "defaults.app", "endpoints.api"} {
			if !strings.Contains(contents, key) {
				t.Fatalf("step 2: expected config file to contain key %q, got %q", key, contents)
			}
		}
	})

	// Step 3: List after the three-key setup. Secret is masked, non-secrets
	// show literal values, exactly three lines of key=... output.
	t.Run("step3_list_masks_secret_shows_plain_for_others", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "config_store_step3_list_masked")

		setToken := sandbox.RunAA(t, []string{"config", "token.flyio=fo1_abc123yourrealtokenhere"}, nil)
		if setToken.ExitCode != 0 {
			t.Fatalf("step 3 precondition: expected exit 0 seeding token, got %d; stderr=%q", setToken.ExitCode, setToken.Stderr)
		}
		setOthers := sandbox.RunAA(t, []string{
			"config",
			"defaults.app=my-team-app",
			"endpoints.api=https://api.staging.fly.io/v1",
		}, nil)
		if setOthers.ExitCode != 0 {
			t.Fatalf("step 3 precondition: expected exit 0 seeding defaults+endpoints, got %d; stderr=%q", setOthers.ExitCode, setOthers.Stderr)
		}

		result := sandbox.RunAA(t, []string{"config"}, nil)
		if result.ExitCode != 0 {
			t.Fatalf("step 3: expected exit 0 listing config, got %d; stderr=%q", result.ExitCode, result.Stderr)
		}

		lines := nonEmptyLines(result.Stdout)
		if len(lines) != 3 {
			t.Fatalf("step 3: expected exactly 3 lines in list output, got %d: %q", len(lines), result.Stdout)
		}

		// Token line is masked per architecture ADR 2: literal "<set>".
		if !strings.Contains(result.Stdout, "token.flyio=<set>") {
			t.Fatalf("step 3: expected masked token line %q in stdout, got %q", "token.flyio=<set>", result.Stdout)
		}
		// Masking must not leak any portion of the real value.
		if strings.Contains(result.Stdout, "fo1_abc123yourrealtokenhere") {
			t.Fatalf("step 3: expected masked list NOT to contain the raw token, got %q", result.Stdout)
		}
		// Non-secret keys render literally.
		if !strings.Contains(result.Stdout, "defaults.app=my-team-app") {
			t.Fatalf("step 3: expected stdout to contain %q, got %q", "defaults.app=my-team-app", result.Stdout)
		}
		if !strings.Contains(result.Stdout, "endpoints.api=https://api.staging.fly.io/v1") {
			t.Fatalf("step 3: expected stdout to contain %q, got %q", "endpoints.api=https://api.staging.fly.io/v1", result.Stdout)
		}
	})

	// Step 4: Remove one key, then re-list and confirm absence.
	t.Run("step4_remove_key_then_absent_on_list", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "config_store_step4_remove_key")

		setToken := sandbox.RunAA(t, []string{"config", "token.flyio=fo1_abc123yourrealtokenhere"}, nil)
		if setToken.ExitCode != 0 {
			t.Fatalf("step 4 precondition: expected exit 0 seeding token, got %d; stderr=%q", setToken.ExitCode, setToken.Stderr)
		}
		setOthers := sandbox.RunAA(t, []string{
			"config",
			"defaults.app=my-team-app",
			"endpoints.api=https://api.staging.fly.io/v1",
		}, nil)
		if setOthers.ExitCode != 0 {
			t.Fatalf("step 4 precondition: expected exit 0 seeding defaults+endpoints, got %d; stderr=%q", setOthers.ExitCode, setOthers.Stderr)
		}

		remove := sandbox.RunAA(t, []string{"config", "--remove", "endpoints.api"}, nil)
		if remove.ExitCode != 0 {
			t.Fatalf("step 4: expected exit 0 removing endpoints.api, got %d; stderr=%q", remove.ExitCode, remove.Stderr)
		}
		if !strings.Contains(remove.Stdout, "removed endpoints.api") {
			t.Fatalf("step 4: expected stdout to contain %q, got %q", "removed endpoints.api", remove.Stdout)
		}

		list := sandbox.RunAA(t, []string{"config"}, nil)
		if list.ExitCode != 0 {
			t.Fatalf("step 4: expected exit 0 listing after remove, got %d; stderr=%q", list.ExitCode, list.Stderr)
		}
		if strings.Contains(list.Stdout, "endpoints.api") {
			t.Fatalf("step 4: expected list after remove NOT to contain %q, got %q", "endpoints.api", list.Stdout)
		}
	})

	// Step 5: Fresh sandbox, no set ever performed. The empty-state line is
	// a user-facing contract per v2/docs/config-store.md.
	t.Run("step5_list_on_fresh_sandbox_prints_no_config_set", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "config_store_step5_empty_list")

		result := sandbox.RunAA(t, []string{"config"}, nil)
		if result.ExitCode != 0 {
			t.Fatalf("step 5: expected exit 0 listing empty config, got %d; stderr=%q", result.ExitCode, result.Stderr)
		}
		if !strings.Contains(result.Stdout, "(no config set)") {
			t.Fatalf("step 5: expected stdout to contain the literal %q, got %q", "(no config set)", result.Stdout)
		}
	})

	// Step 6: Command that needs the token with no token stored. Must fail
	// loud and name token.flyio plus the remediation command.
	t.Run("step6_machine_ls_without_token_names_the_key", func(t *testing.T) {
		sandbox := testhelpers.NewSandbox(t, "config_store_step6_missing_token_error")

		result := sandbox.RunAA(t, []string{"machine", "ls"}, nil)
		if result.ExitCode == 0 {
			t.Fatalf("step 6: expected non-zero exit when token is unset, got 0; stdout=%q stderr=%q", result.Stdout, result.Stderr)
		}
		if !strings.Contains(result.Stderr, "token.flyio") {
			t.Fatalf("step 6: expected stderr to name the key %q, got %q", "token.flyio", result.Stderr)
		}
		if !strings.Contains(result.Stderr, "aa config token.flyio=") {
			t.Fatalf("step 6: expected stderr to tell the user how to set the key via %q, got %q", "aa config token.flyio=", result.Stderr)
		}
	})
}

// nonEmptyLines splits s on newlines and drops empty / whitespace-only entries.
// Used to count list-output lines without coupling to trailing-newline details.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
