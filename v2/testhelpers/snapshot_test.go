package testhelpers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// These meta-tests pin snapshot JSON read/write behavior per ADR-5/ADR-6:
//   - save then load produces identical entries, in order
//   - empty snapshot is valid
//   - multiple surfaces ("api", "registry") may interleave in one file
//   - record-mode write overwrites any prior content

func TestSnapshot_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rt.json")

	entries := []snapshotEntry{
		{
			Surface: "api",
			Request: recordedRequest{Method: "GET", Path: "/v1/apps/foo"},
			Response: recordedResponse{
				Status: 200,
				Body:   []byte(`{"name":"foo"}`),
			},
		},
		{
			Surface: "api",
			Request: recordedRequest{Method: "POST", Path: "/v1/apps/foo/machines",
				Body: []byte(`{"name":"m1"}`)},
			Response: recordedResponse{Status: 201, Body: []byte(`{"id":"d8e7"}`)},
		},
	}

	if err := saveSnapshot(path, entries); err != nil {
		t.Fatalf("saveSnapshot failed: %v", err)
	}

	loaded, err := loadSnapshot(path)
	if err != nil {
		t.Fatalf("loadSnapshot failed: %v", err)
	}
	if !reflect.DeepEqual(loaded, entries) {
		t.Fatalf("round-trip mismatch.\nwant: %#v\ngot:  %#v", entries, loaded)
	}
}

func TestSnapshot_EmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := saveSnapshot(path, []snapshotEntry{}); err != nil {
		t.Fatalf("saveSnapshot empty failed: %v", err)
	}
	loaded, err := loadSnapshot(path)
	if err != nil {
		t.Fatalf("loadSnapshot empty failed: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected empty slice, got %d entries", len(loaded))
	}
}

func TestSnapshot_MultipleSurfacesInterleaved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.json")

	entries := []snapshotEntry{
		{Surface: "api", Request: recordedRequest{Method: "GET", Path: "/v1/apps/x"},
			Response: recordedResponse{Status: 200}},
		{Surface: "registry", Request: recordedRequest{Method: "GET", Path: "/v2/x/manifests/latest"},
			Response: recordedResponse{Status: 200}},
		{Surface: "api", Request: recordedRequest{Method: "POST", Path: "/v1/apps/x/machines"},
			Response: recordedResponse{Status: 201}},
		{Surface: "registry", Request: recordedRequest{Method: "PUT", Path: "/v2/x/blobs/uploads/"},
			Response: recordedResponse{Status: 202}},
	}

	if err := saveSnapshot(path, entries); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	loaded, err := loadSnapshot(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if len(loaded) != len(entries) {
		t.Fatalf("len mismatch: want %d got %d", len(entries), len(loaded))
	}
	for i, e := range entries {
		if loaded[i].Surface != e.Surface {
			t.Fatalf("entry %d surface: want %q got %q", i, e.Surface, loaded[i].Surface)
		}
		if loaded[i].Request.Path != e.Request.Path {
			t.Fatalf("entry %d path mismatch", i)
		}
	}
}

func TestSnapshot_RecordModeOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overwrite.json")

	first := []snapshotEntry{{
		Surface:  "api",
		Request:  recordedRequest{Method: "GET", Path: "/old"},
		Response: recordedResponse{Status: 404},
	}}
	if err := saveSnapshot(path, first); err != nil {
		t.Fatalf("first save failed: %v", err)
	}

	second := []snapshotEntry{{
		Surface:  "api",
		Request:  recordedRequest{Method: "GET", Path: "/new"},
		Response: recordedResponse{Status: 200},
	}}
	if err := saveSnapshot(path, second); err != nil {
		t.Fatalf("overwrite save failed: %v", err)
	}

	loaded, err := loadSnapshot(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected exactly 1 entry after overwrite, got %d", len(loaded))
	}
	if loaded[0].Request.Path != "/new" {
		t.Fatalf("expected /new after overwrite, got %q", loaded[0].Request.Path)
	}
}

func TestSnapshot_LoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")
	if _, err := loadSnapshot(path); err == nil {
		t.Fatalf("expected error loading missing file")
	}
}

func TestSnapshot_LoadMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0o644); err != nil {
		t.Fatalf("seed write failed: %v", err)
	}
	if _, err := loadSnapshot(path); err == nil {
		t.Fatalf("expected error loading malformed JSON")
	}
}
