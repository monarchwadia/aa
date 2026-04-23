package main

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

// Evaluate applies the configured rules to the list of files changed by a
// patch and returns every violation, in rule-declaration order.
//
// Pure function. No I/O, no config loading; callers pass the already-
// resolved rule list and the already-parsed file list.
//
// Implemented in rules_engine.go (wave 1 workstream `rules-engine`).
func Evaluate(rules []Rule, changedFiles []string) []RuleViolation {
	panic("unimplemented — see workstream `rules-engine` in docs/architecture/aa.md § Workstreams")
}
