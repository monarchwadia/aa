package testhelpers

// Request-matching logic for replay mode (ADR-1):
//   - method: exact
//   - path: exact
//   - query: parsed and compared as a multimap (order-insensitive, duplicates
//     significant)
//   - body: JSON-canonicalized when both sides are JSON, else byte-exact;
//     a nil/empty snapshot body means "ignore incoming body"
//   - headers: ignored entirely

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
)

// matchRequest compares an expected (snapshot) request against an actual
// incoming request under the ADR-1 rules. Returns "" on match, a short
// diagnostic string on mismatch naming the first differing field.
//
// Example:
//
//	diff := matchRequest(snap.Request, actual)
//	// diff == "" on match, or e.g. "method: expected GET got POST"
func matchRequest(expected, actual recordedRequest) string {
	if expected.Method != actual.Method {
		return fmt.Sprintf("method: expected %s got %s", expected.Method, actual.Method)
	}
	if expected.Path != actual.Path {
		return fmt.Sprintf("path: expected %s got %s", expected.Path, actual.Path)
	}
	if d := compareQuery(expected.Query, actual.Query); d != "" {
		return "query: " + d
	}
	if d := compareBody(expected.Body, actual.Body); d != "" {
		return "body: " + d
	}
	return ""
}

func compareQuery(expected, actual string) string {
	want, err1 := url.ParseQuery(expected)
	got, err2 := url.ParseQuery(actual)
	if err1 != nil || err2 != nil {
		if expected != actual {
			return fmt.Sprintf("expected %q got %q", expected, actual)
		}
		return ""
	}
	if len(want) != len(got) {
		return fmt.Sprintf("expected %q got %q", expected, actual)
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			return fmt.Sprintf("missing key %q", k)
		}
		wc := append([]string(nil), wv...)
		gc := append([]string(nil), gv...)
		sort.Strings(wc)
		sort.Strings(gc)
		if len(wc) != len(gc) {
			return fmt.Sprintf("key %q value count: expected %d got %d", k, len(wc), len(gc))
		}
		for i := range wc {
			if wc[i] != gc[i] {
				return fmt.Sprintf("key %q: expected %v got %v", k, wv, gv)
			}
		}
	}
	return ""
}

func compareBody(expected, actual []byte) string {
	// ADR-1: empty/nil snapshot body means the snapshot author declared the
	// body non-load-bearing; ignore whatever came in.
	if len(expected) == 0 {
		return ""
	}
	expCanon, expOK := canonicalizeJSON(expected)
	actCanon, actOK := canonicalizeJSON(actual)
	if expOK && actOK {
		if bytes.Equal(expCanon, actCanon) {
			return ""
		}
		return fmt.Sprintf("expected %s got %s", expCanon, actCanon)
	}
	if bytes.Equal(expected, actual) {
		return ""
	}
	return fmt.Sprintf("expected %q got %q", expected, actual)
}

func canonicalizeJSON(in []byte) ([]byte, bool) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	out, err := json.Marshal(canonicalValue(v))
	if err != nil {
		return nil, false
	}
	return out, true
}

func canonicalValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]kv, 0, len(keys))
		for _, k := range keys {
			out = append(out, kv{K: k, V: canonicalValue(x[k])})
		}
		return canonicalMap(out)
	case []any:
		for i := range x {
			x[i] = canonicalValue(x[i])
		}
		return x
	}
	return v
}

// kv is used to preserve sorted order when marshaling a map as JSON; Go's
// json package sorts map keys by string but that's already lexicographic, so
// we rebuild through a plain map and rely on encoding/json's sort. The detour
// exists only to recurse into nested maps. Simpler path: just return the
// sorted-key map directly.
type kv struct {
	K string
	V any
}

type canonicalMap []kv

func (m canonicalMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, e := range m {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(e.K)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(e.V)
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
