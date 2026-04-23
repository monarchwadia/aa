package testhelpers

// Snapshot JSON schema and read/write helpers. The snapshot is the source of
// truth in replay mode and the sink in record mode (ADR-5). One file per
// test, stored under v2/testdata/snapshots/<name>.json.

import (
	"encoding/json"
	"fmt"
	"os"
)

// recordedRequest is the on-disk schema for a recorded HTTP request.
type recordedRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// recordedResponse is the on-disk schema for a recorded HTTP response.
type recordedResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// snapshotEntry is one exchange in a snapshot file. Surface is either "api"
// (Fly Machines API) or "registry" (Fly container registry), per the arch
// doc's 2026-04-23 amendment.
type snapshotEntry struct {
	Surface  string           `json:"surface"`
	Request  recordedRequest  `json:"request"`
	Response recordedResponse `json:"response"`
}

// loadSnapshot reads the snapshot file at path and returns its entries in the
// order they were recorded.
//
// Example:
//
//	entries, err := loadSnapshot("v2/testdata/snapshots/TestSpawnHappyPath.json")
//	// entries[0].Request.Method == "GET"
func loadSnapshot(path string) ([]snapshotEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load snapshot %s: %w", path, err)
	}
	var entries []snapshotEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse snapshot %s: %w", path, err)
	}
	return entries, nil
}

// saveSnapshot serializes entries as indented JSON and writes them to path,
// overwriting any prior content (ADR-5 — record mode is a hammer).
//
// Example:
//
//	_ = saveSnapshot("TestSpawnHappyPath.json", captured)
func saveSnapshot(path string, entries []snapshotEntry) error {
	if entries == nil {
		entries = []snapshotEntry{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
