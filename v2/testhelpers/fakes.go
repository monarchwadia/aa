package testhelpers

// This file holds the small data types that describe fake external binaries
// and their observed invocations. Everything here is value-only; behavior
// lives in fakebin.go and sandbox.go.

// Invocation is one recorded call to a fake external binary captured by the
// sandbox during a RunAA call.
//
// Example:
//
//	invs := sandbox.BinaryInvocations("flyctl")
//	// invs[0].Argv == []string{"ssh", "console", "--app", "my-app", "--machine", "d8e7"}
type Invocation struct {
	Argv  []string
	Env   map[string]string
	Stdin []byte
}

// FakeBinary is a declarative description of a fake external binary the
// sandbox plants on PATH so the real aa process can shell out to it offline.
//
// Example:
//
//	sandbox.ExpectBinary("flyctl",
//	    WantArgs("ssh", "console", "--app", "my-app", "--machine", "d8e7"),
//	    RespondExitCode(0),
//	    RespondStdout(""),
//	)
type FakeBinary struct {
	Name     string
	ExitCode int
	Stdout   string
	Stderr   string
}

// FakeBinaryOption configures an expected fake-binary invocation declared via
// Sandbox.ExpectBinary.
type FakeBinaryOption func(*fakeBinaryConfig)

type fakeBinaryConfig struct {
	wantArgs      []string
	respondExit   int
	respondStdout string
	respondStderr string
}

// WantArgs asserts the fake binary is invoked with exactly these argv values,
// positional, in order. Assertion is performed by the test itself against
// Sandbox.BinaryInvocations after RunAA returns.
func WantArgs(args ...string) FakeBinaryOption {
	return func(c *fakeBinaryConfig) { c.wantArgs = args }
}

// RespondExitCode makes the fake binary exit with the given code on every
// invocation. Defaults to 0 when the option is omitted.
func RespondExitCode(code int) FakeBinaryOption {
	return func(c *fakeBinaryConfig) { c.respondExit = code }
}

// RespondStdout makes the fake binary print the given string to stdout.
func RespondStdout(s string) FakeBinaryOption {
	return func(c *fakeBinaryConfig) { c.respondStdout = s }
}

// RespondStderr makes the fake binary print the given string to stderr.
func RespondStderr(s string) FakeBinaryOption {
	return func(c *fakeBinaryConfig) { c.respondStderr = s }
}
