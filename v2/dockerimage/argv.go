// Package dockerimage — argv.go: argv construction for the docker subprocess.
//
// Every shell-out to `docker` goes through one of these constructors so the
// test harness can assert the exact argv sequence, and so argv is always a
// slice (never a shell string).
package dockerimage

import "aa/v2/imageref"

// BuildArgv returns the argv for `docker build -t <tag> <contextPath>`.
func BuildArgv(ref imageref.ImageRef, contextPath string) []string {
	return []string{"build", "-t", ref.String(), contextPath}
}

// PushArgv returns the argv for `docker push <tag>`.
func PushArgv(ref imageref.ImageRef) []string {
	return []string{"push", ref.String()}
}

// LoginArgv returns the argv for `docker login <host> -u x -p <token>`.
// The token is placed in argv exactly once; no further docker call receives it.
func LoginArgv(host, token string) []string {
	return []string{"login", host, "-u", "x", "-p", token}
}

// InspectArgv returns the argv for `docker image inspect <tag>`.
func InspectArgv(ref imageref.ImageRef) []string {
	return []string{"image", "inspect", ref.String()}
}
