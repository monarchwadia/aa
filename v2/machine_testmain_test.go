// machine_testmain_test.go isolates HOME from the developer's real user
// config for every test in package main that constructs a
// configstore.Reader without explicitly setting t.Setenv("HOME", ...).
//
// The ResolveFlyToken unit tests in machine_handlers_test.go construct
// configstore.NewReader(nil) and expect an empty file layer. Without this
// isolation they'd pick up the developer's real ~/.config/aa/config on
// disk, which (on any machine where `aa config token.flyio=...` has ever
// been run) already carries a token.
//
// This is a test-only file; it does not affect the production binary.
package main

import (
	"os"
	"path/filepath"
)

func init() {
	// Only isolate HOME when the test binary is actually running tests.
	// The config_cmd_test.go subprocess re-exec path (AA_CONFIG_CMD_REEXEC=1)
	// deliberately ships its own HOME/XDG_CONFIG_HOME via cmd.Env and relies
	// on those values; overriding them here breaks those subprocess tests.
	if os.Getenv("AA_CONFIG_CMD_REEXEC") == "1" {
		return
	}
	tmp, err := os.MkdirTemp("", "aa-machine-tests-home-")
	if err != nil {
		return
	}
	os.Setenv("HOME", tmp)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
}
