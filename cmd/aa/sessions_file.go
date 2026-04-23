package main

import "path/filepath"

// FileSessionStore is the default SessionStore implementation. It persists
// session records as JSON files under <BaseDir>/<id>.json, where BaseDir is
// ~/.aa/sessions on a real laptop and a t.TempDir()-relative path in tests.
//
// Save writes the record atomically via write-to-temp-then-rename so a crash
// mid-write cannot corrupt an existing file. Load returns (zero, false, nil)
// for a missing ID. Delete is idempotent. List skips non-JSON files.
//
// All method bodies are currently panic stubs; see workstream `session-store`
// in docs/architecture/aa.md § Workstreams.
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

// Save atomically persists rec to <BaseDir>/<rec.ID>.json, overwriting any
// prior record with the same ID.
func (s *FileSessionStore) Save(rec LocalSessionRecord) error {
	panic("unimplemented — see workstream `session-store` in docs/architecture/aa.md § Workstreams")
}

// Load reads the record at <BaseDir>/<id>.json. Returns (zero, false, nil)
// if no file exists for that ID.
func (s *FileSessionStore) Load(id SessionID) (LocalSessionRecord, bool, error) {
	panic("unimplemented — see workstream `session-store` in docs/architecture/aa.md § Workstreams")
}

// Delete removes <BaseDir>/<id>.json. Returns nil if the file does not
// exist (idempotent).
func (s *FileSessionStore) Delete(id SessionID) error {
	panic("unimplemented — see workstream `session-store` in docs/architecture/aa.md § Workstreams")
}

// List returns every record whose file lives in BaseDir and ends in `.json`.
// Non-JSON files are skipped. Order is unspecified.
func (s *FileSessionStore) List() ([]LocalSessionRecord, error) {
	panic("unimplemented — see workstream `session-store` in docs/architecture/aa.md § Workstreams")
}
