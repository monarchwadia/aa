// Package dockerimage — argv.go: argv construction for the docker subprocess.
//
// Every shell-out to `docker` goes through one of these constructors so the
// test harness can assert the exact argv sequence, and so argv is always a
// slice (never a shell string).
package dockerimage

// BuildArgv returns the argv for `docker build -t <tag> <contextPath>`.
//
// Example: BuildArgv(ImageRef{Host:"registry.fly.io", Repo:"aa-apps/myapi", Reference:"latest"}, "./myapi")
// returns []string{"build", "-t", "registry.fly.io/aa-apps/myapi:latest", "./myapi"}.
func BuildArgv(ref ImageRef, contextPath string) []string {
	return []string{"build", "-t", ref.String(), contextPath}
}

// PushArgv returns the argv for `docker push <tag>`.
//
// Example: PushArgv(ImageRef{Host:"registry.fly.io", Repo:"aa-apps/myapi", Reference:"latest"})
// returns []string{"push", "registry.fly.io/aa-apps/myapi:latest"}.
func PushArgv(ref ImageRef) []string {
	return []string{"push", ref.String()}
}

// LoginArgv returns the argv for `docker login <host> -u x -p <token>`.
// The token is placed in argv exactly once; no further docker call receives it.
//
// Example: LoginArgv("registry.fly.io", "fo1_abc") returns
// []string{"login", "registry.fly.io", "-u", "x", "-p", "fo1_abc"}.
func LoginArgv(host, token string) []string {
	return []string{"login", host, "-u", "x", "-p", token}
}

// InspectArgv returns the argv for `docker image inspect <tag>`.
//
// Example: InspectArgv(ImageRef{Host:"registry.fly.io", Repo:"aa-apps/myapi", Reference:"latest"})
// returns []string{"image", "inspect", "registry.fly.io/aa-apps/myapi:latest"}.
func InspectArgv(ref ImageRef) []string {
	return []string{"image", "inspect", ref.String()}
}
