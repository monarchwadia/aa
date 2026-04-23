// Package dockerimage — argv_test.go: unit tests for docker argv construction.
//
// Pins the exact argv each verb produces. No subprocess is spawned; these
// are pure-function tests. argv is always a slice — never a shell string —
// which these tests pin via adversarial inputs.
package dockerimage

import (
	"reflect"
	"strings"
	"testing"

	"aa/v2/imageref"
)

func tagRef() imageref.ImageRef {
	return imageref.ImageRef{Host: "registry.fly.io", Repo: "aa-apps/myapi", Reference: "latest"}
}

// BuildArgv produces `build -t <fq-tag> <context>` (exact slice).
func TestBuildArgvExactSlice(t *testing.T) {
	got := BuildArgv(tagRef(), "./myapi")
	want := []string{"build", "-t", "registry.fly.io/aa-apps/myapi:latest", "./myapi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// BuildArgv passes an absolute context path through unchanged.
func TestBuildArgvAbsoluteContextPath(t *testing.T) {
	got := BuildArgv(tagRef(), "/home/alice/hello")
	if got[len(got)-1] != "/home/alice/hello" {
		t.Fatalf("last arg = %q", got[len(got)-1])
	}
}

// PushArgv produces exactly `push <fq-tag>`.
func TestPushArgvExactSlice(t *testing.T) {
	got := PushArgv(tagRef())
	want := []string{"push", "registry.fly.io/aa-apps/myapi:latest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// LoginArgv produces exactly `login <host> -u x -p <token>`.
func TestLoginArgvExactSlice(t *testing.T) {
	got := LoginArgv("registry.fly.io", "SECRET-TOKEN")
	want := []string{"login", "registry.fly.io", "-u", "x", "-p", "SECRET-TOKEN"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// InspectArgv produces exactly `image inspect <fq-tag>`.
func TestInspectArgvExactSlice(t *testing.T) {
	got := InspectArgv(tagRef())
	want := []string{"image", "inspect", "registry.fly.io/aa-apps/myapi:latest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// Tags with shell-metacharacters remain a single argv slot — the invariant
// argv construction buys over shell-string composition.
func TestBuildArgvTagWithShellMetaStaysOneSlot(t *testing.T) {
	nastyRef := imageref.ImageRef{Host: "registry.fly.io", Repo: "aa-apps/a;b", Reference: "latest"}
	got := BuildArgv(nastyRef, "./ctx")
	if got[2] != "registry.fly.io/aa-apps/a;b:latest" {
		t.Fatalf("tag arg split or mangled: %v", got)
	}
}

// LoginArgv keeps the token in a single argv slot even with whitespace.
func TestLoginArgvTokenSingleSlot(t *testing.T) {
	got := LoginArgv("registry.fly.io", "tok en with spaces")
	if got[len(got)-1] != "tok en with spaces" {
		t.Fatalf("token split across slots: %v", got)
	}
}

// BuildArgv never emits a shell-concatenated slot.
func TestBuildArgvNoShellStringLeak(t *testing.T) {
	got := BuildArgv(tagRef(), "./myapi")
	for _, a := range got {
		if strings.Contains(a, " ") {
			t.Errorf("argv slot contains whitespace, suggests shell composition: %q", a)
		}
	}
}
