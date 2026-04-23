package main

import "strings"

// RuleViolation is one rule matched against a set of changed files in a
// patch, together with the severity the user chose for it.
type RuleViolation struct {
	// Rule is the rule definition that matched.
	Rule Rule

	// MatchedFiles are the changed-file paths from the patch that matched
	// this rule's globs, in the order they appeared in the patch.
	MatchedFiles []string

	// Explanation is the rule's human-readable docstring — the "attacks
	// this rule guards against" text rendered under the violation banner
	// in `aa push`. Built-in rules carry their own; user-defined
	// fileChanged rules use a brief generic message.
	Explanation string
}

// builtInGlobs maps each built-in rule type to the file globs it watches.
// The spec lives in README § "Rules (patch safeguards)". fileChanged is
// absent: its globs come from the user's Rule.Include.
var builtInGlobs = map[string][]string{
	"gitHooksChanged": {".githooks/**", ".husky/**", ".gitattributes"},
	"ciConfigChanged": {
		".github/workflows/**", ".gitlab-ci.yml", ".circleci/**",
		"azure-pipelines.yml", ".drone.yml",
	},
	"packageManifestChanged": {
		"package.json", "pyproject.toml", "setup.py",
		"Cargo.toml", "Gemfile", "go.mod",
	},
	"lockfileChanged": {
		"package-lock.json", "yarn.lock", "pnpm-lock.yaml",
		"poetry.lock", "Cargo.lock", "Gemfile.lock",
	},
	"dockerfileChanged":  {"**/Dockerfile", "docker-compose*.yml"},
	"buildScriptChanged": {"Makefile", "justfile", "Taskfile.yml", "scripts/**"},
}

// builtInExplanations is the short human-readable text rendered under each
// violation banner. Keys are rule types; the fallback is the generic message
// used for fileChanged.
var builtInExplanations = map[string]string{
	"gitHooksChanged":        "Committed git hooks and filters execute on contributor machines after pull.",
	"ciConfigChanged":        "CI configuration runs with deploy credentials; a classic supply-chain pivot.",
	"packageManifestChanged": "Package manifests can run install hooks and scripts on `npm install`/equivalents.",
	"lockfileChanged":        "Lockfile changes can pin malicious dependency versions.",
	"dockerfileChanged":      "Dockerfiles execute arbitrary code at build time.",
	"buildScriptChanged":     "Build scripts are invoked by common `make install`-style workflows.",
}

const fileChangedExplanation = "Matches user-supplied include globs for paths the repo wants extra scrutiny on."

// globsForRule returns the glob patterns a given rule matches against. For
// built-in types it's a fixed set from the spec; for fileChanged it's the
// user's Include list.
func globsForRule(rule Rule) []string {
	if rule.Type == "fileChanged" {
		return rule.Include
	}
	return builtInGlobs[rule.Type]
}

// explanationForRule returns the human-readable docstring keyed by rule type.
func explanationForRule(rule Rule) string {
	if text, ok := builtInExplanations[rule.Type]; ok {
		return text
	}
	return fileChangedExplanation
}

// matchGlob reports whether path matches pattern, where pattern may contain
// `**` (any run of path segments including empty), `*` (anything except `/`),
// and `?` (any single character except `/`). filepath.Match alone cannot do
// this because it rejects `**`.
//
// Example: matchGlob("**/Dockerfile", "services/app/Dockerfile") == true.
func matchGlob(pattern, path string) bool {
	return matchGlobAt(pattern, 0, path, 0)
}

// matchGlobAt is the recursive matcher. pi / si are byte offsets into
// pattern / path. Recursion depth is bounded by the number of `**` segments
// in the pattern, which is at most a handful for our built-in globs.
func matchGlobAt(pattern string, pi int, path string, si int) bool {
	for pi < len(pattern) {
		switch pattern[pi] {
		case '*':
			// Detect `**` (optionally followed by `/`): consumes any run of
			// characters including path separators.
			if pi+1 < len(pattern) && pattern[pi+1] == '*' {
				rest := pi + 2
				if rest < len(pattern) && pattern[rest] == '/' {
					rest++
				}
				// Try consuming 0..len(path)-si characters.
				for k := si; k <= len(path); k++ {
					if matchGlobAt(pattern, rest, path, k) {
						return true
					}
				}
				return false
			}
			// Single `*`: any run not crossing `/`.
			rest := pi + 1
			for k := si; k <= len(path); k++ {
				if matchGlobAt(pattern, rest, path, k) {
					return true
				}
				if k < len(path) && path[k] == '/' {
					break
				}
			}
			return false
		case '?':
			if si >= len(path) || path[si] == '/' {
				return false
			}
			pi++
			si++
		default:
			if si >= len(path) || pattern[pi] != path[si] {
				return false
			}
			pi++
			si++
		}
	}
	return si == len(path)
}

// anyGlobMatches reports whether any of the globs matches path.
func anyGlobMatches(globs []string, path string) bool {
	for _, g := range globs {
		if matchGlob(g, path) {
			return true
		}
	}
	return false
}

// Evaluate applies the configured rules to the list of files changed by a
// patch and returns every violation, in rule-declaration order.
//
// Pure function. No I/O, no config loading; callers pass the already-
// resolved rule list and the already-parsed file list.
//
// Example:
//
//	rules := []Rule{{Type: "gitHooksChanged", Severity: SeverityError}}
//	got := Evaluate(rules, []string{".githooks/pre-commit", "README.md"})
//	// got[0].MatchedFiles == []string{".githooks/pre-commit"}
func Evaluate(rules []Rule, changedFiles []string) []RuleViolation {
	// No allocation for the zero-match common case: the result slice is
	// created lazily the first time a violation is appended.
	var violations []RuleViolation

	for _, rule := range rules {
		if rule.Severity == SeverityOff {
			continue
		}
		globs := globsForRule(rule)
		if len(globs) == 0 {
			continue
		}

		var matched []string
		for _, file := range changedFiles {
			// Skip empty strings defensively — fuzz input may include them
			// and they're never a meaningful path.
			if file == "" || strings.ContainsRune(file, 0) {
				continue
			}
			if anyGlobMatches(globs, file) {
				matched = append(matched, file)
			}
		}
		if len(matched) == 0 {
			continue
		}
		violations = append(violations, RuleViolation{
			Rule:         rule,
			MatchedFiles: matched,
			Explanation:  explanationForRule(rule),
		})
	}
	return violations
}
