// aa-proxy is a small HTTP CONNECT proxy that enforces a hostname allowlist.
// aa cross-compiles it for linux/{amd64,arm64} and scp's the matching binary
// to each agent host during egress install.
//
// See docs/architecture/aa.md § "Decision 3" for why this ships in-tree
// rather than using tinyproxy or a similar external proxy.
package main

// main is wired in the `proxy-binary` workstream (wave 1).
// Until then this is a placeholder so the package builds.
func main() {
	panic("unimplemented — see workstream `proxy-binary` in docs/architecture/aa.md § Workstreams")
}
