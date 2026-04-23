package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileSessionStore is the default SessionStore implementation. It persists
// session records as JSON files under <BaseDir>/<id>.json, where BaseDir is
// ~/.aa/sessions on a real laptop and a t.TempDir()-relative path in tests.
//
// Save writes the record atomically via write-to-temp-then-rename so a crash
// mid-write cannot corrupt an existing file. Load returns (zero, false, nil)
// for a missing ID. Delete is idempotent. List skips non-JSON files.
//
// Records are pretty-printed JSON (2-space indent) so the solo developer can
// `cat ~/.aa/sessions/<id>.json` and read the state directly — see PHILOSOPHY
// axis 3 (Observability).
type FileSessionStore struct {
	BaseDir string
}

// NewFileSessionStore returns a FileSessionStore rooted at
// <homeDir>/.aa/sessions. It does NOT create the directory; Save is
// responsible for creating it on first write.
func NewFileSessionStore(homeDir string) *FileSessionStore {
	return &FileSessionStore{
		BaseDir: filepath.Join(homeDir, ".aa", "sessions"),
	}
}

// recordPath returns the canonical on-disk path for the given session ID.
func (s *FileSessionStore) recordPath(id SessionID) string {
	return filepath.Join(s.BaseDir, string(id)+".json")
}

// Save atomically persists rec to <BaseDir>/<rec.ID>.json, overwriting any
// prior record with the same ID. It creates BaseDir if it does not yet exist.
//
// Atomicity comes from writing to <id>.json.tmp first and then renaming into
// place. os.Rename is atomic on the same filesystem, so a crash between the
// write and the rename leaves the previous file intact; a crash after the
// rename leaves the new file intact. No partially-written record is ever
// observable as <id>.json.
func (s *FileSessionStore) Save(rec LocalSessionRecord) error {
	if err := os.MkdirAll(s.BaseDir, 0o700); err != nil {
		return fmt.Errorf("creating session dir %s: %w", s.BaseDir, err)
	}

	encoded, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session %s: %w", rec.ID, err)
	}

	finalPath := s.recordPath(rec.ID)
	tmpPath := finalPath + ".tmp"

	if err := os.WriteFile(tmpPath, encoded, 0o600); err != nil {
		return fmt.Errorf("writing temp file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Best-effort cleanup of the temp file; the rename failure is the
		// real error to surface.
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming %s -> %s: %w", tmpPath, finalPath, err)
	}

	return nil
}

// Load reads the record at <BaseDir>/<id>.json. Returns (zero, false, nil)
// if no file exists for that ID, and (zero, false, err) on malformed JSON or
// any other I/O error.
func (s *FileSessionStore) Load(id SessionID) (LocalSessionRecord, bool, error) {
	path := s.recordPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LocalSessionRecord{}, false, nil
		}
		return LocalSessionRecord{}, false, fmt.Errorf("reading session %s: %w", path, err)
	}

	var rec LocalSessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return LocalSessionRecord{}, false, fmt.Errorf("parsing session %s: %w", path, err)
	}
	return rec, true, nil
}

// Delete removes <BaseDir>/<id>.json. Returns nil if the file does not
// exist (idempotent).
func (s *FileSessionStore) Delete(id SessionID) error {
	path := s.recordPath(id)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("deleting session %s: %w", path, err)
	}
	return nil
}

// List returns every record whose file lives in BaseDir and ends in `.json`.
// Non-JSON files, subdirectories, and dotfiles (e.g. `.hidden.json`) are
// skipped. A missing BaseDir yields an empty slice and a nil error — the
// store simply hasn't been written to yet.
func (s *FileSessionStore) List() ([]LocalSessionRecord, error) {
	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing session dir %s: %w", s.BaseDir, err)
	}

	records := make([]LocalSessionRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}

		path := filepath.Join(s.BaseDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading session %s: %w", path, err)
		}
		var rec LocalSessionRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, fmt.Errorf("parsing session %s: %w", path, err)
		}
		records = append(records, rec)
	}
	return records, nil
}
