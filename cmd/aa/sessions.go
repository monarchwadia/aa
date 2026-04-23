package main

// SessionStore persists laptop-side session records. The default
// implementation (FileSessionStore in sessions_file.go) writes one file per
// session under ~/.aa/sessions/<id>.json. Tests can substitute an
// in-memory fake via testfakes.
type SessionStore interface {
	// Save atomically persists the record. Overwrites any prior record
	// with the same ID.
	Save(rec LocalSessionRecord) error

	// Load fetches the record with the given ID. Returns (empty, false, nil)
	// if no record exists with that ID.
	Load(id SessionID) (LocalSessionRecord, bool, error)

	// Delete removes the record with the given ID. Idempotent: deleting a
	// non-existent record is a no-op, not an error.
	Delete(id SessionID) error

	// List returns every persisted record. Used by `aa list` and
	// `aa sweep`. Order is unspecified.
	List() ([]LocalSessionRecord, error)
}
