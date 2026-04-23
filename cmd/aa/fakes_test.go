package main

import (
	"context"
	"errors"
	"io"
	"sync"
)

// This file holds shared test fakes for every interface in the contract
// files. It lives in a *_test.go file so the fakes are compiled only for
// tests — never shipped in the production binary.
//
// Append-only by convention: when a workstream needs a new fake, add it
// here with a new exported type and constructor. Never edit an existing
// fake's signature.

// ---------------------------------------------------------------------------
// fakeBackend is a programmable Backend. Tests set behavior per method via
// function fields; default behaviors are reasonable no-ops.
// ---------------------------------------------------------------------------

type fakeBackend struct {
	mu sync.Mutex

	ProvisionFn        func(ctx context.Context, id SessionID) (Host, error)
	InstallEgressFn    func(ctx context.Context, host Host, allowlist []string) error
	RunContainerFn     func(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error)
	ReadRemoteFileFn   func(ctx context.Context, host Host, relpath string) ([]byte, error)
	StreamLogsFn       func(ctx context.Context, host Host, relpath string, w io.Writer) error
	TeardownFn         func(ctx context.Context, host Host) error

	// Call logs, in observation order.
	ProvisionCalls     []SessionID
	InstallEgressCalls []struct {
		Host      Host
		Allowlist []string
	}
	RunContainerCalls []ContainerSpec
	ReadFileCalls     []string
	StreamLogsCalls   []string
	TeardownCalls     []Host
}

func newFakeBackend() *fakeBackend { return &fakeBackend{} }

func (f *fakeBackend) Provision(ctx context.Context, id SessionID) (Host, error) {
	f.mu.Lock()
	f.ProvisionCalls = append(f.ProvisionCalls, id)
	f.mu.Unlock()
	if f.ProvisionFn != nil {
		return f.ProvisionFn(ctx, id)
	}
	return Host{BackendType: "fake", Workspace: "/workspace"}, nil
}

func (f *fakeBackend) InstallEgress(ctx context.Context, host Host, allowlist []string) error {
	f.mu.Lock()
	f.InstallEgressCalls = append(f.InstallEgressCalls, struct {
		Host      Host
		Allowlist []string
	}{host, append([]string(nil), allowlist...)})
	f.mu.Unlock()
	if f.InstallEgressFn != nil {
		return f.InstallEgressFn(ctx, host, allowlist)
	}
	return nil
}

func (f *fakeBackend) RunContainer(ctx context.Context, host Host, spec ContainerSpec) (ContainerHandle, error) {
	f.mu.Lock()
	f.RunContainerCalls = append(f.RunContainerCalls, spec)
	f.mu.Unlock()
	if f.RunContainerFn != nil {
		return f.RunContainerFn(ctx, host, spec)
	}
	return ContainerHandle{ID: "fake-container", Host: host}, nil
}

func (f *fakeBackend) ReadRemoteFile(ctx context.Context, host Host, relpath string) ([]byte, error) {
	f.mu.Lock()
	f.ReadFileCalls = append(f.ReadFileCalls, relpath)
	f.mu.Unlock()
	if f.ReadRemoteFileFn != nil {
		return f.ReadRemoteFileFn(ctx, host, relpath)
	}
	return nil, errors.New("fakeBackend.ReadRemoteFile: no ReadRemoteFileFn set")
}

func (f *fakeBackend) StreamLogs(ctx context.Context, host Host, relpath string, w io.Writer) error {
	f.mu.Lock()
	f.StreamLogsCalls = append(f.StreamLogsCalls, relpath)
	f.mu.Unlock()
	if f.StreamLogsFn != nil {
		return f.StreamLogsFn(ctx, host, relpath, w)
	}
	return nil
}

func (f *fakeBackend) Teardown(ctx context.Context, host Host) error {
	f.mu.Lock()
	f.TeardownCalls = append(f.TeardownCalls, host)
	f.mu.Unlock()
	if f.TeardownFn != nil {
		return f.TeardownFn(ctx, host)
	}
	return nil
}

// ---------------------------------------------------------------------------
// fakeSessionStore is an in-memory SessionStore for tests.
// ---------------------------------------------------------------------------

type fakeSessionStore struct {
	mu      sync.Mutex
	records map[SessionID]LocalSessionRecord
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{records: map[SessionID]LocalSessionRecord{}}
}

func (s *fakeSessionStore) Save(rec LocalSessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[rec.ID] = rec
	return nil
}

func (s *fakeSessionStore) Load(id SessionID) (LocalSessionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	return rec, ok, nil
}

func (s *fakeSessionStore) Delete(id SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, id)
	return nil
}

func (s *fakeSessionStore) List() ([]LocalSessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LocalSessionRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// fakeEphemeralKeyProvider mints deterministic test keys and records every
// Mint/Revoke call so tests can assert lifecycle correctness.
// ---------------------------------------------------------------------------

type fakeEphemeralKeyProvider struct {
	mu     sync.Mutex
	nextID int

	MintedLiveByID map[string]bool // true if minted and not yet revoked
	MintCalls      []MintRequest
	RevokeCalls    []KeyHandle
}

func newFakeEphemeralKeyProvider() *fakeEphemeralKeyProvider {
	return &fakeEphemeralKeyProvider{MintedLiveByID: map[string]bool{}}
}

func (p *fakeEphemeralKeyProvider) Mint(ctx context.Context, req MintRequest) (KeyHandle, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	id := fakeKeyID(p.nextID)
	p.MintedLiveByID[id] = true
	p.MintCalls = append(p.MintCalls, req)
	return KeyHandle{Provider: "fake", ID: id}, "sk-fake-" + id, nil
}

func (p *fakeEphemeralKeyProvider) Revoke(ctx context.Context, handle KeyHandle) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.RevokeCalls = append(p.RevokeCalls, handle)
	p.MintedLiveByID[handle.ID] = false
	return nil
}

func fakeKeyID(n int) string {
	return "fake-key-" + itoa(n)
}

// itoa is a tiny stdlib-free int-to-string for test code.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// fakeSSHRunner records invocations and lets tests program responses.
// ---------------------------------------------------------------------------

type fakeSSHRunner struct {
	mu sync.Mutex

	RunFn    func(ctx context.Context, host Host, cmd string) (SSHResult, error)
	AttachFn func(ctx context.Context, host Host, cmd string, stdin io.Reader, stdout, stderr io.Writer) error
	CopyFn   func(ctx context.Context, host Host, src, dst string) error

	RunCalls    []string
	AttachCalls []string
	CopyCalls   []struct{ Src, Dst string }
}

func newFakeSSHRunner() *fakeSSHRunner { return &fakeSSHRunner{} }

func (f *fakeSSHRunner) Run(ctx context.Context, host Host, cmd string) (SSHResult, error) {
	f.mu.Lock()
	f.RunCalls = append(f.RunCalls, cmd)
	f.mu.Unlock()
	if f.RunFn != nil {
		return f.RunFn(ctx, host, cmd)
	}
	return SSHResult{}, nil
}

func (f *fakeSSHRunner) Attach(ctx context.Context, host Host, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	f.mu.Lock()
	f.AttachCalls = append(f.AttachCalls, cmd)
	f.mu.Unlock()
	if f.AttachFn != nil {
		return f.AttachFn(ctx, host, cmd, stdin, stdout, stderr)
	}
	return nil
}

func (f *fakeSSHRunner) Copy(ctx context.Context, host Host, src, dst string) error {
	f.mu.Lock()
	f.CopyCalls = append(f.CopyCalls, struct{ Src, Dst string }{src, dst})
	f.mu.Unlock()
	if f.CopyFn != nil {
		return f.CopyFn(ctx, host, src, dst)
	}
	return nil
}
