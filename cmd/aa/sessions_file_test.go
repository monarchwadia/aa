package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestStore returns a FileSessionStore rooted at a fresh t.TempDir().
// No real ~/.aa is touched.
func newTestStore(t *testing.T) (*FileSessionStore, string) {
	t.Helper()
	home := t.TempDir()
	return NewFileSessionStore(home), home
}

// sampleRecord returns a fully-populated LocalSessionRecord with the given ID
// so round-trip tests exercise every field the README mentions.
func sampleRecord(id SessionID) LocalSessionRecord {
	created := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	pushed := time.Date(2026, 4, 23, 12, 30, 0, 0, time.UTC)
	tornDown := time.Date(2026, 4, 23, 13, 0, 0, 0, time.UTC)
	return LocalSessionRecord{
		ID:      id,
		Repo:    "/Users/monarch/code/myapp",
		Branch:  "feature/oauth",
		Backend: "fly",
		Host: Host{
			Address:     "ubuntu@203.0.113.7:22",
			BackendType: "fly",
			Workspace:   "/home/fly/workspace",
		},
		EphemeralKeyID: "aa-myapp-feature-oauth-1714000000",
		CreatedAt:      created,
		PushedAt:       &pushed,
		TornDownAt:     &tornDown,
	}
}

// recordsEqual deep-compares two LocalSessionRecord values including the
// time pointers, which reflect.DeepEqual handles correctly for *time.Time
// once both sides have been round-tripped through JSON.
func recordsEqual(a, b LocalSessionRecord) bool {
	// JSON drops monotonic clock; normalize both sides via UTC/Unix to make
	// failures readable but still correct.
	normalize := func(r LocalSessionRecord) LocalSessionRecord {
		r.CreatedAt = r.CreatedAt.UTC()
		if r.PushedAt != nil {
			v := r.PushedAt.UTC()
			r.PushedAt = &v
		}
		if r.TornDownAt != nil {
			v := r.TornDownAt.UTC()
			r.TornDownAt = &v
		}
		return r
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

// ---------------------------------------------------------------------------
// Behavior tests
// ---------------------------------------------------------------------------

func TestFileSessionStore_SaveThenLoad_RoundTrips(t *testing.T) {
	store, _ := newTestStore(t)
	rec := sampleRecord("myapp-feature-oauth")

	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := store.Load(rec.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatalf("Load: ok = false, want true")
	}
	if !recordsEqual(got, rec) {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", got, rec)
	}
}

func TestFileSessionStore_Load_MissingID_ReturnsZeroFalseNil(t *testing.T) {
	store, _ := newTestStore(t)

	got, ok, err := store.Load("nonexistent-id")
	if err != nil {
		t.Fatalf("Load: err = %v, want nil", err)
	}
	if ok {
		t.Fatalf("Load: ok = true, want false")
	}
	if !reflect.DeepEqual(got, LocalSessionRecord{}) {
		t.Fatalf("Load: got = %+v, want zero value", got)
	}
}

func TestFileSessionStore_Save_SameID_Overwrites(t *testing.T) {
	store, _ := newTestStore(t)
	id := SessionID("myapp-feature-oauth")

	first := sampleRecord(id)
	first.Branch = "feature/oauth"
	if err := store.Save(first); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	second := sampleRecord(id)
	second.Branch = "feature/oauth-v2"
	second.EphemeralKeyID = "aa-myapp-second"
	if err := store.Save(second); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, ok, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatalf("Load: ok = false, want true")
	}
	if got.Branch != "feature/oauth-v2" {
		t.Fatalf("Branch: got %q, want %q (overwrite failed)", got.Branch, "feature/oauth-v2")
	}
	if got.EphemeralKeyID != "aa-myapp-second" {
		t.Fatalf("EphemeralKeyID: got %q, want %q (overwrite failed)", got.EphemeralKeyID, "aa-myapp-second")
	}
}

// TestFileSessionStore_Save_IsAtomic_DocumentsIntendedBehavior documents that
// Save must use write-to-temp-then-rename semantics so a crash mid-write
// cannot corrupt an existing valid file. This test makes the target
// directory unwritable to simulate a mid-write failure, then asserts the
// previously-saved record is still loadable and intact.
//
// Implementation may differ in exactly how atomicity is achieved — the
// behavior this test pins down is: a failing Save must not leave the
// on-disk record half-written.
func TestFileSessionStore_Save_IsAtomic_DocumentsIntendedBehavior(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}

	store, _ := newTestStore(t)
	id := SessionID("myapp-feature-oauth")
	original := sampleRecord(id)
	if err := store.Save(original); err != nil {
		t.Fatalf("initial Save: %v", err)
	}

	// Make BaseDir read-only to simulate a mid-write failure on the
	// rename/create step of the next Save.
	if err := os.Chmod(store.BaseDir, 0o500); err != nil {
		t.Fatalf("chmod BaseDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(store.BaseDir, 0o700) })

	// This Save may or may not error depending on implementation; the
	// load-bearing assertion is that the original file remains valid.
	mutated := sampleRecord(id)
	mutated.Branch = "feature/should-not-appear"
	_ = store.Save(mutated)

	// Restore perms so Load can read.
	if err := os.Chmod(store.BaseDir, 0o700); err != nil {
		t.Fatalf("chmod restore: %v", err)
	}

	got, ok, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load after failed Save: err = %v, want nil (file should remain valid)", err)
	}
	if !ok {
		t.Fatalf("Load after failed Save: ok = false, want true (file should remain)")
	}
	if got.Branch == "feature/should-not-appear" {
		// If the mutated branch leaked through, that's actually fine — it means
		// the Save succeeded. The failure mode we're guarding against is a
		// corrupted/half-written file, not a successful overwrite.
		t.Logf("Save succeeded despite read-only dir; atomicity not exercised on this platform")
	} else if !recordsEqual(got, original) {
		t.Fatalf("original record corrupted by failed Save:\n got  %+v\n want %+v", got, original)
	}
}

func TestFileSessionStore_Delete_RemovesFile(t *testing.T) {
	store, _ := newTestStore(t)
	rec := sampleRecord("myapp-feature-oauth")

	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, ok, err := store.Load(rec.ID)
	if err != nil {
		t.Fatalf("Load after Delete: err = %v, want nil", err)
	}
	if ok {
		t.Fatalf("Load after Delete: ok = true, want false")
	}
}

func TestFileSessionStore_Delete_Nonexistent_IsIdempotent(t *testing.T) {
	store, _ := newTestStore(t)

	if err := store.Delete("never-existed"); err != nil {
		t.Fatalf("Delete of missing ID: err = %v, want nil (idempotent)", err)
	}
}

func TestFileSessionStore_List_Empty_ReturnsEmptyOrNil(t *testing.T) {
	store, _ := newTestStore(t)

	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List on empty dir: got %d records, want 0", len(got))
	}
}

func TestFileSessionStore_List_NRecords_ReturnsAll(t *testing.T) {
	store, _ := newTestStore(t)

	ids := []SessionID{
		"myapp-feature-oauth",
		"myapp-feature-sso",
		"other-repo-main",
	}
	for _, id := range ids {
		if err := store.Save(sampleRecord(id)); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("List: got %d records, want %d", len(got), len(ids))
	}

	gotIDs := make([]string, 0, len(got))
	for _, r := range got {
		gotIDs = append(gotIDs, string(r.ID))
	}
	sort.Strings(gotIDs)

	wantIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		wantIDs = append(wantIDs, string(id))
	}
	sort.Strings(wantIDs)

	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("List IDs:\n got  %v\n want %v (order unspecified; compared as sets)", gotIDs, wantIDs)
	}
}

func TestFileSessionStore_List_SkipsNonJSONFiles(t *testing.T) {
	store, _ := newTestStore(t)

	// Save one real record so BaseDir exists.
	rec := sampleRecord("myapp-feature-oauth")
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Drop some non-JSON junk alongside it: a README, a stray .txt, a
	// subdirectory. A future feature might put any of these in
	// ~/.aa/sessions/; List must ignore them.
	junk := map[string][]byte{
		"README":       []byte("notes"),
		"notes.txt":    []byte("not json"),
		"scratch.tmp":  []byte("half-written"),
		".hidden.json": []byte(`{"id":"hidden"}`), // hidden: implementation-defined, but should not crash List
	}
	for name, body := range junk {
		if err := os.WriteFile(filepath.Join(store.BaseDir, name), body, 0o600); err != nil {
			t.Fatalf("write junk %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(store.BaseDir, "subdir"), 0o700); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// At minimum, the real record must be present. Implementations may
	// decide whether .hidden.json counts; the guarantee this test pins down
	// is that non-.json entries and subdirectories do NOT crash and do NOT
	// appear as spurious records with empty IDs.
	foundReal := false
	for _, r := range got {
		if r.ID == rec.ID {
			foundReal = true
		}
		if r.ID == "" {
			t.Fatalf("List surfaced a record with empty ID (likely parsed non-JSON junk): %+v", r)
		}
	}
	if !foundReal {
		t.Fatalf("List did not return the real record; got %+v", got)
	}
}

// TestFileSessionStore_JSONFormat_MatchesReadmeFields writes a record by hand
// using the field names the README/state.go specify (id, repo, branch,
// backend, host, ephemeral_key_id, created_at, pushed_at, torn_down_at) and
// confirms Load parses it correctly. This is the on-disk-contract test.
func TestFileSessionStore_JSONFormat_MatchesReadmeFields(t *testing.T) {
	store, _ := newTestStore(t)

	if err := os.MkdirAll(store.BaseDir, 0o700); err != nil {
		t.Fatalf("mkdir BaseDir: %v", err)
	}

	id := SessionID("myapp-feature-oauth")
	handWritten := `{
  "id": "myapp-feature-oauth",
  "repo": "/Users/monarch/code/myapp",
  "branch": "feature/oauth",
  "backend": "fly",
  "host": {
    "Address": "ubuntu@203.0.113.7:22",
    "BackendType": "fly",
    "Workspace": "/home/fly/workspace"
  },
  "ephemeral_key_id": "aa-myapp-feature-oauth-1714000000",
  "created_at": "2026-04-23T10:00:00Z",
  "pushed_at":  "2026-04-23T12:30:00Z",
  "torn_down_at": "2026-04-23T13:00:00Z"
}`
	path := filepath.Join(store.BaseDir, string(id)+".json")
	if err := os.WriteFile(path, []byte(handWritten), 0o600); err != nil {
		t.Fatalf("write hand-crafted JSON: %v", err)
	}

	got, ok, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load hand-crafted JSON: %v", err)
	}
	if !ok {
		t.Fatalf("Load hand-crafted JSON: ok = false, want true")
	}

	if got.ID != id {
		t.Errorf("ID: got %q, want %q", got.ID, id)
	}
	if got.Repo != "/Users/monarch/code/myapp" {
		t.Errorf("Repo: got %q", got.Repo)
	}
	if got.Branch != "feature/oauth" {
		t.Errorf("Branch: got %q", got.Branch)
	}
	if got.Backend != "fly" {
		t.Errorf("Backend: got %q", got.Backend)
	}
	if got.Host.Address != "ubuntu@203.0.113.7:22" {
		t.Errorf("Host.Address: got %q", got.Host.Address)
	}
	if got.EphemeralKeyID != "aa-myapp-feature-oauth-1714000000" {
		t.Errorf("EphemeralKeyID: got %q", got.EphemeralKeyID)
	}
	wantCreated := time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC)
	if !got.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, wantCreated)
	}
	if got.PushedAt == nil || !got.PushedAt.Equal(time.Date(2026, 4, 23, 12, 30, 0, 0, time.UTC)) {
		t.Errorf("PushedAt: got %v", got.PushedAt)
	}
	if got.TornDownAt == nil || !got.TornDownAt.Equal(time.Date(2026, 4, 23, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("TornDownAt: got %v", got.TornDownAt)
	}

	// Also assert the reverse: encoding our own sampleRecord produces JSON
	// with the same field names, so `aa` and hand-editors agree on wire
	// format.
	encoded, err := json.Marshal(sampleRecord(id))
	if err != nil {
		t.Fatalf("marshal sampleRecord: %v", err)
	}
	for _, field := range []string{
		`"id":`, `"repo":`, `"branch":`, `"backend":`, `"host":`,
		`"ephemeral_key_id":`, `"created_at":`, `"pushed_at":`, `"torn_down_at":`,
	} {
		if !containsString(string(encoded), field) {
			t.Errorf("marshaled record missing field %s; got %s", field, encoded)
		}
	}
}

func TestFileSessionStore_Save_CreatesDirectoryIfMissing(t *testing.T) {
	home := t.TempDir()
	// Deliberately do NOT create ~/.aa/sessions; Save must do it.
	store := NewFileSessionStore(home)

	if _, err := os.Stat(store.BaseDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: BaseDir should not exist yet; stat err = %v", err)
	}

	rec := sampleRecord("myapp-feature-oauth")
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save into fresh home: %v", err)
	}

	got, ok, err := store.Load(rec.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatalf("Load: ok = false after Save into fresh home")
	}
	if got.ID != rec.ID {
		t.Fatalf("Load: ID = %q, want %q", got.ID, rec.ID)
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestFileSessionStore_ConcurrentSaveDifferentIDs_NoDataLoss(t *testing.T) {
	store, _ := newTestStore(t)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)

	errs := make(chan error, n)
	panics := make(chan any, n)
	for i := range n {
		go func() {
			defer wg.Done()
			// Recover per-goroutine so a stub panic doesn't kill the whole
			// test binary before we can observe it. When the real
			// FileSessionStore lands, no goroutine should panic and this
			// recover is a no-op.
			defer func() {
				if r := recover(); r != nil {
					panics <- r
				}
			}()
			rec := sampleRecord(SessionID(concurrentID(i)))
			rec.Branch = concurrentID(i) + "-branch"
			if err := store.Save(rec); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(panics)
	for p := range panics {
		t.Fatalf("concurrent Save panicked: %v", p)
	}
	for err := range errs {
		t.Fatalf("concurrent Save: %v", err)
	}

	// Every record must be loadable and carry the right per-goroutine data.
	for i := range n {
		id := SessionID(concurrentID(i))
		got, ok, err := store.Load(id)
		if err != nil {
			t.Fatalf("Load %s: %v", id, err)
		}
		if !ok {
			t.Fatalf("Load %s: ok = false (data lost)", id)
		}
		if got.Branch != concurrentID(i)+"-branch" {
			t.Fatalf("Load %s: Branch = %q, want %q (wrong goroutine's data)",
				id, got.Branch, concurrentID(i)+"-branch")
		}
	}

	// List must show all N.
	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != n {
		t.Fatalf("List: got %d records, want %d", len(all), n)
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func concurrentID(i int) string {
	return "session-" + strconv.Itoa(i)
}

// containsString wraps strings.Contains. Kept as a named helper so the intent
// ("assert the encoded JSON mentions this field name") reads clearly at the
// call site.
func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
