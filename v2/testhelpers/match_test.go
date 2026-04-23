package testhelpers

import "testing"

// These meta-tests pin the HTTP request matching rules defined in
// docs/architecture/test-harness.md ADR-1:
//   - method: exact
//   - path: exact
//   - query: order-insensitive multimap compare
//   - body: JSON-canonicalized when both sides are JSON, else byte-exact;
//           snapshot null body ignores the incoming body
//   - headers: ignored except Authorization (which is scrubbed anyway)

func TestMatchRequest_ExactMethodAndPath(t *testing.T) {
	expected := recordedRequest{Method: "GET", Path: "/v1/apps/foo"}
	actual := recordedRequest{Method: "GET", Path: "/v1/apps/foo"}
	if diff := matchRequest(expected, actual); diff != "" {
		t.Fatalf("expected match, got diff: %s", diff)
	}
}

func TestMatchRequest_MethodMismatch(t *testing.T) {
	expected := recordedRequest{Method: "GET", Path: "/v1/apps/foo"}
	actual := recordedRequest{Method: "POST", Path: "/v1/apps/foo"}
	if diff := matchRequest(expected, actual); diff == "" {
		t.Fatalf("expected method mismatch to be reported")
	}
}

func TestMatchRequest_PathMismatch(t *testing.T) {
	expected := recordedRequest{Method: "GET", Path: "/v1/apps/foo"}
	actual := recordedRequest{Method: "GET", Path: "/v1/apps/bar"}
	if diff := matchRequest(expected, actual); diff == "" {
		t.Fatalf("expected path mismatch to be reported")
	}
}

func TestMatchRequest_QueryOrderInsensitive(t *testing.T) {
	expected := recordedRequest{Method: "DELETE", Path: "/v1/machines/x", Query: "force=true&region=iad"}
	actual := recordedRequest{Method: "DELETE", Path: "/v1/machines/x", Query: "region=iad&force=true"}
	if diff := matchRequest(expected, actual); diff != "" {
		t.Fatalf("query reorder should match, got diff: %s", diff)
	}
}

func TestMatchRequest_QueryDuplicateValuesDistinguished(t *testing.T) {
	expected := recordedRequest{Method: "GET", Path: "/v1/x", Query: "tag=a&tag=b"}
	actual := recordedRequest{Method: "GET", Path: "/v1/x", Query: "tag=a&tag=a"}
	if diff := matchRequest(expected, actual); diff == "" {
		t.Fatalf("duplicate value set should differ")
	}
}

func TestMatchRequest_BodyJSONKeyOrderInsensitive(t *testing.T) {
	expected := recordedRequest{
		Method: "POST", Path: "/v1/apps/foo/machines",
		Body: []byte(`{"name":"m1","config":{"image":"u:22"}}`),
	}
	actual := recordedRequest{
		Method: "POST", Path: "/v1/apps/foo/machines",
		Body: []byte(`{"config":{"image":"u:22"},"name":"m1"}`),
	}
	if diff := matchRequest(expected, actual); diff != "" {
		t.Fatalf("JSON key reorder should match, got diff: %s", diff)
	}
}

func TestMatchRequest_BodyJSONValueMismatch(t *testing.T) {
	expected := recordedRequest{Method: "POST", Path: "/p", Body: []byte(`{"a":1}`)}
	actual := recordedRequest{Method: "POST", Path: "/p", Body: []byte(`{"a":2}`)}
	if diff := matchRequest(expected, actual); diff == "" {
		t.Fatalf("JSON value mismatch should differ")
	}
}

func TestMatchRequest_SnapshotNullBodyIgnoresIncomingBody(t *testing.T) {
	expected := recordedRequest{Method: "GET", Path: "/v1/apps/foo", Body: nil}
	actual := recordedRequest{Method: "GET", Path: "/v1/apps/foo", Body: []byte(`{"anything":"goes"}`)}
	if diff := matchRequest(expected, actual); diff != "" {
		t.Fatalf("null snapshot body should ignore incoming body, got diff: %s", diff)
	}
}

func TestMatchRequest_NonJSONBodyExactByteCompare(t *testing.T) {
	expected := recordedRequest{Method: "POST", Path: "/p", Body: []byte("hello world")}
	actual := recordedRequest{Method: "POST", Path: "/p", Body: []byte("hello world")}
	if diff := matchRequest(expected, actual); diff != "" {
		t.Fatalf("exact non-JSON body should match, got diff: %s", diff)
	}
	actual2 := recordedRequest{Method: "POST", Path: "/p", Body: []byte("hello WORLD")}
	if diff := matchRequest(expected, actual2); diff == "" {
		t.Fatalf("differing non-JSON body should differ")
	}
}

func TestMatchRequest_HeadersIgnoredExceptIrrelevant(t *testing.T) {
	expected := recordedRequest{
		Method: "GET", Path: "/v1/apps/foo",
		Headers: map[string]string{"X-Request-Id": "abc"},
	}
	actual := recordedRequest{
		Method: "GET", Path: "/v1/apps/foo",
		Headers: map[string]string{"X-Request-Id": "xyz", "User-Agent": "different"},
	}
	if diff := matchRequest(expected, actual); diff != "" {
		t.Fatalf("headers should be ignored for matching, got diff: %s", diff)
	}
}
