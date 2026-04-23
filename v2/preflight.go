package main

import (
	"fmt"
	"os/exec"
)

func preflight() error {
	if _, err := exec.LookPath("flyctl"); err != nil {
		return fmt.Errorf("flyctl not found in PATH — install it from https://fly.io/docs/flyctl/install/")
	}
	return nil
}
