package main

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isSubset returns true if every element of sub appears in super.
func isSubset(sub, super []string) bool {
	seen := make(map[string]int)
	for _, s := range super {
		seen[s]++
	}
	for _, s := range sub {
		if seen[s] == 0 {
			return false
		}
		seen[s]--
	}
	return true
}

// sameStringSet returns true if a and b contain the same elements regardless
// of order. Used where rule-declaration order is not the property under test.
func sameStringSet(a, b []string) bool {
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	return reflect.DeepEqual(ac, bc)
}

// ruleTypes returns the Type field of each RuleViolation's Rule, in order —
// used to assert rule-declaration ordering of the output.
func ruleTypes(vs []RuleViolation) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Rule.Type
	}
	return out
}

// ---------------------------------------------------------------------------
// Built-in rule matching — one subtest per built-in type.
//
// Each case configures a single rule of the given built-in type at severity
// "error" and asserts that every listed changed file ends up in the single
// resulting RuleViolation's MatchedFiles set.
// ---------------------------------------------------------------------------

func TestEvaluate_BuiltInRuleMatching(t *testing.T) {
	cases := []struct {
		name         string
		ruleType     string
		changedFiles []string
		wantMatched  []string
	}{
		{
			name:         "gitHooksChanged/githooks-dir",
			ruleType:     "gitHooksChanged",
			changedFiles: []string{".githooks/pre-commit"},
			wantMatched:  []string{".githooks/pre-commit"},
		},
		{
			name:         "gitHooksChanged/husky-dir",
			ruleType:     "gitHooksChanged",
			changedFiles: []string{".husky/pre-push"},
			wantMatched:  []string{".husky/pre-push"},
		},
		{
			name:         "gitHooksChanged/gitattributes",
			ruleType:     "gitHooksChanged",
			changedFiles: []string{".gitattributes"},
			wantMatched:  []string{".gitattributes"},
		},

		{
			name:         "ciConfigChanged/github-workflows",
			ruleType:     "ciConfigChanged",
			changedFiles: []string{".github/workflows/deploy.yml"},
			wantMatched:  []string{".github/workflows/deploy.yml"},
		},
		{
			name:         "ciConfigChanged/gitlab-ci",
			ruleType:     "ciConfigChanged",
			changedFiles: []string{".gitlab-ci.yml"},
			wantMatched:  []string{".gitlab-ci.yml"},
		},
		{
			name:         "ciConfigChanged/circleci",
			ruleType:     "ciConfigChanged",
			changedFiles: []string{".circleci/config.yml"},
			wantMatched:  []string{".circleci/config.yml"},
		},

		{
			name:         "packageManifestChanged/package-json",
			ruleType:     "packageManifestChanged",
			changedFiles: []string{"package.json"},
			wantMatched:  []string{"package.json"},
		},
		{
			name:         "packageManifestChanged/go-mod",
			ruleType:     "packageManifestChanged",
			changedFiles: []string{"go.mod"},
			wantMatched:  []string{"go.mod"},
		},
		{
			name:         "packageManifestChanged/cargo-toml",
			ruleType:     "packageManifestChanged",
			changedFiles: []string{"Cargo.toml"},
			wantMatched:  []string{"Cargo.toml"},
		},

		{
			name:         "lockfileChanged/package-lock",
			ruleType:     "lockfileChanged",
			changedFiles: []string{"package-lock.json"},
			wantMatched:  []string{"package-lock.json"},
		},
		{
			name:         "lockfileChanged/cargo-lock",
			ruleType:     "lockfileChanged",
			changedFiles: []string{"Cargo.lock"},
			wantMatched:  []string{"Cargo.lock"},
		},
		{
			name:         "lockfileChanged/gemfile-lock",
			ruleType:     "lockfileChanged",
			changedFiles: []string{"Gemfile.lock"},
			wantMatched:  []string{"Gemfile.lock"},
		},

		{
			name:         "dockerfileChanged/root-dockerfile",
			ruleType:     "dockerfileChanged",
			changedFiles: []string{"Dockerfile"},
			wantMatched:  []string{"Dockerfile"},
		},
		{
			name:         "dockerfileChanged/nested-dockerfile",
			ruleType:     "dockerfileChanged",
			changedFiles: []string{"services/app/Dockerfile"},
			wantMatched:  []string{"services/app/Dockerfile"},
		},
		{
			name:         "dockerfileChanged/compose",
			ruleType:     "dockerfileChanged",
			changedFiles: []string{"docker-compose.yml"},
			wantMatched:  []string{"docker-compose.yml"},
		},
		{
			name:         "dockerfileChanged/compose-variant",
			ruleType:     "dockerfileChanged",
			changedFiles: []string{"docker-compose.prod.yml"},
			wantMatched:  []string{"docker-compose.prod.yml"},
		},

		{
			name:         "buildScriptChanged/makefile",
			ruleType:     "buildScriptChanged",
			changedFiles: []string{"Makefile"},
			wantMatched:  []string{"Makefile"},
		},
		{
			name:         "buildScriptChanged/scripts-dir",
			ruleType:     "buildScriptChanged",
			changedFiles: []string{"scripts/install.sh"},
			wantMatched:  []string{"scripts/install.sh"},
		},
		{
			name:         "buildScriptChanged/taskfile",
			ruleType:     "buildScriptChanged",
			changedFiles: []string{"Taskfile.yml"},
			wantMatched:  []string{"Taskfile.yml"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := []Rule{{Type: tc.ruleType, Severity: SeverityError}}

			got := Evaluate(rules, tc.changedFiles)

			if len(got) != 1 {
				t.Fatalf("expected exactly 1 violation, got %d (%#v)", len(got), got)
			}
			if got[0].Rule.Type != tc.ruleType {
				t.Errorf("violation rule type = %q, want %q", got[0].Rule.Type, tc.ruleType)
			}
			if !sameStringSet(got[0].MatchedFiles, tc.wantMatched) {
				t.Errorf("MatchedFiles = %v, want %v", got[0].MatchedFiles, tc.wantMatched)
			}
			if !isSubset(got[0].MatchedFiles, tc.changedFiles) {
				t.Errorf("MatchedFiles %v is not a subset of changedFiles %v",
					got[0].MatchedFiles, tc.changedFiles)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// fileChanged (user-defined) with Include globs
// ---------------------------------------------------------------------------

func TestEvaluate_FileChanged_UserDefinedInclude(t *testing.T) {
	rules := []Rule{
		{
			Type:     "fileChanged",
			Severity: SeverityError,
			Include:  []string{"infra/**", "terraform/**"},
		},
	}
	changedFiles := []string{"infra/main.tf", "src/app.ts"}

	got := Evaluate(rules, changedFiles)

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 violation, got %d", len(got))
	}
	want := []string{"infra/main.tf"}
	if !sameStringSet(got[0].MatchedFiles, want) {
		t.Errorf("MatchedFiles = %v, want %v", got[0].MatchedFiles, want)
	}
	if !isSubset(got[0].MatchedFiles, changedFiles) {
		t.Errorf("MatchedFiles %v is not a subset of changedFiles %v",
			got[0].MatchedFiles, changedFiles)
	}
}

// ---------------------------------------------------------------------------
// Multiple rules, multiple files — ordering is by rule declaration.
// ---------------------------------------------------------------------------

func TestEvaluate_MultipleRulesReturnInDeclarationOrder(t *testing.T) {
	rules := []Rule{
		{Type: "packageManifestChanged", Severity: SeverityWarn},
		{Type: "gitHooksChanged", Severity: SeverityError},
	}
	changedFiles := []string{
		"package.json",
		".githooks/pre-commit",
	}

	got := Evaluate(rules, changedFiles)

	if len(got) != 2 {
		t.Fatalf("expected 2 violations, got %d (%#v)", len(got), got)
	}
	gotTypes := ruleTypes(got)
	wantTypes := []string{"packageManifestChanged", "gitHooksChanged"}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Errorf("violation order = %v, want %v", gotTypes, wantTypes)
	}

	// Every matched file must be in the original changedFiles set.
	for _, v := range got {
		if !isSubset(v.MatchedFiles, changedFiles) {
			t.Errorf("violation %q MatchedFiles %v is not a subset of %v",
				v.Rule.Type, v.MatchedFiles, changedFiles)
		}
	}
}

// ---------------------------------------------------------------------------
// Nothing matches — returns empty (or nil).
// ---------------------------------------------------------------------------

func TestEvaluate_NothingMatchesReturnsEmpty(t *testing.T) {
	rules := []Rule{
		{Type: "gitHooksChanged", Severity: SeverityError},
		{Type: "ciConfigChanged", Severity: SeverityError},
		{Type: "packageManifestChanged", Severity: SeverityWarn},
	}
	changedFiles := []string{"src/hello.ts"}

	got := Evaluate(rules, changedFiles)

	if len(got) != 0 {
		t.Errorf("expected no violations, got %d (%#v)", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// Severity "off" is ignored even when files match.
// ---------------------------------------------------------------------------

func TestEvaluate_SeverityOffIsIgnored(t *testing.T) {
	rules := []Rule{
		{Type: "gitHooksChanged", Severity: SeverityOff},
		{Type: "packageManifestChanged", Severity: SeverityWarn},
	}
	changedFiles := []string{
		".githooks/pre-commit", // would match the "off" rule
		"package.json",         // matches the warn rule
	}

	got := Evaluate(rules, changedFiles)

	if len(got) != 1 {
		t.Fatalf("expected 1 violation (off rule must be ignored), got %d (%#v)",
			len(got), got)
	}
	if got[0].Rule.Type != "packageManifestChanged" {
		t.Errorf("expected packageManifestChanged violation, got %q", got[0].Rule.Type)
	}
	// Sanity: the ignored rule must not appear.
	for _, v := range got {
		if v.Rule.Type == "gitHooksChanged" {
			t.Errorf("off-severity rule leaked into violations: %#v", v)
		}
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestEvaluate_EdgeCases(t *testing.T) {
	t.Run("empty rules returns empty", func(t *testing.T) {
		got := Evaluate(nil, []string{"package.json", ".githooks/pre-commit"})
		if len(got) != 0 {
			t.Errorf("expected 0 violations for empty rules, got %d", len(got))
		}
	})

	t.Run("empty changedFiles returns empty", func(t *testing.T) {
		rules := []Rule{
			{Type: "gitHooksChanged", Severity: SeverityError},
			{Type: "packageManifestChanged", Severity: SeverityWarn},
		}
		got := Evaluate(rules, nil)
		if len(got) != 0 {
			t.Errorf("expected 0 violations for empty changedFiles, got %d", len(got))
		}
	})

	t.Run("both empty returns empty", func(t *testing.T) {
		got := Evaluate(nil, nil)
		if len(got) != 0 {
			t.Errorf("expected 0 violations for empty/empty, got %d", len(got))
		}
	})

	t.Run("same file matching two rules appears in both violations", func(t *testing.T) {
		// Cargo.toml is a package manifest. Arrange a second user rule that
		// also matches it via fileChanged + an explicit include, so the same
		// path appears in two separate violations.
		rules := []Rule{
			{Type: "packageManifestChanged", Severity: SeverityWarn},
			{
				Type:     "fileChanged",
				Severity: SeverityError,
				Include:  []string{"Cargo.toml"},
			},
		}
		changedFiles := []string{"Cargo.toml"}

		got := Evaluate(rules, changedFiles)

		if len(got) != 2 {
			t.Fatalf("expected 2 violations, got %d (%#v)", len(got), got)
		}
		for _, v := range got {
			if !sameStringSet(v.MatchedFiles, []string{"Cargo.toml"}) {
				t.Errorf("violation %q MatchedFiles = %v, want [Cargo.toml]",
					v.Rule.Type, v.MatchedFiles)
			}
		}
		// Declaration order preserved.
		wantTypes := []string{"packageManifestChanged", "fileChanged"}
		if !reflect.DeepEqual(ruleTypes(got), wantTypes) {
			t.Errorf("order = %v, want %v", ruleTypes(got), wantTypes)
		}
	})

	// fileChanged without any Include globs: there are no patterns to match
	// against, so no file should ever match, and the rule should produce no
	// violation. Documented here as the expected "no-op" behaviour — a
	// misconfigured fileChanged rule fails safe (nothing), not loudly.
	t.Run("fileChanged with no Include is a no-op", func(t *testing.T) {
		rules := []Rule{
			{Type: "fileChanged", Severity: SeverityError},
		}
		changedFiles := []string{
			"src/app.ts",
			"infra/main.tf",
			"Cargo.toml",
		}
		got := Evaluate(rules, changedFiles)
		if len(got) != 0 {
			t.Errorf("fileChanged with no Include should produce no violations, got %d (%#v)",
				len(got), got)
		}
	})
}

// ---------------------------------------------------------------------------
// Fuzz — invariants that must hold for ANY input.
// ---------------------------------------------------------------------------

func FuzzEvaluate(f *testing.F) {
	// Seed corpus: a handful of representative paths.
	f.Add(".githooks/pre-commit|package.json|src/app.ts")
	f.Add("Dockerfile|Makefile|scripts/install.sh|README.md")
	f.Add(".github/workflows/deploy.yml|Cargo.lock|infra/main.tf")
	f.Add("")
	f.Add("some/arbitrary/weird\\path\\with\\backslashes")
	f.Add("just-one-file")

	// A stable rule set covering every built-in plus a fileChanged escape
	// hatch, so the fuzzed file list can plausibly match any rule.
	ruleSet := []Rule{
		{Type: "gitHooksChanged", Severity: SeverityError},
		{Type: "ciConfigChanged", Severity: SeverityError},
		{Type: "packageManifestChanged", Severity: SeverityWarn},
		{Type: "lockfileChanged", Severity: SeverityWarn},
		{Type: "dockerfileChanged", Severity: SeverityWarn},
		{Type: "buildScriptChanged", Severity: SeverityWarn},
		{Type: "fileChanged", Severity: SeverityError, Include: []string{"infra/**", "terraform/**"}},
	}
	declOrder := ruleTypes([]RuleViolation{
		{Rule: ruleSet[0]}, {Rule: ruleSet[1]}, {Rule: ruleSet[2]},
		{Rule: ruleSet[3]}, {Rule: ruleSet[4]}, {Rule: ruleSet[5]},
		{Rule: ruleSet[6]},
	})

	f.Fuzz(func(t *testing.T, joined string) {
		// Decode the fuzzer's single-string input into a []string of file
		// paths, skipping empty entries so we don't synthesize "" paths.
		var changedFiles []string
		for _, p := range strings.Split(joined, "|") {
			if p == "" {
				continue
			}
			changedFiles = append(changedFiles, p)
		}

		// Invariant 1: must not panic on any input.
		got := Evaluate(ruleSet, changedFiles)

		// Invariant 2: every MatchedFiles set is a subset of the input.
		for _, v := range got {
			if !isSubset(v.MatchedFiles, changedFiles) {
				t.Fatalf("MatchedFiles %v not a subset of changedFiles %v (rule %q)",
					v.MatchedFiles, changedFiles, v.Rule.Type)
			}
		}

		// Invariant 3: output order matches rule-declaration order. Each
		// violation must appear in the same relative order the rule does in
		// ruleSet. We do NOT require every rule to produce a violation —
		// only that the violations that exist are a subsequence of declOrder.
		idx := 0
		for _, v := range got {
			found := false
			for idx < len(declOrder) {
				if declOrder[idx] == v.Rule.Type {
					found = true
					idx++
					break
				}
				idx++
			}
			if !found {
				t.Fatalf("violation %q breaks rule-declaration order; got types %v, declOrder %v",
					v.Rule.Type, ruleTypes(got), declOrder)
			}
		}
	})
}
