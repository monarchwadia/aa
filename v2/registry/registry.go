// Package registry — registry.go: Docker Registry v2 HTTP client.
//
// Implements the Registry interface against the Docker Registry v2 protocol:
//   - GET  /v2/_catalog                      → list repositories
//   - GET  /v2/<repo>/tags/list              → list tags for a repo
//   - HEAD /v2/<repo>/manifests/<reference>  → resolve tag → digest
//   - DELETE /v2/<repo>/manifests/<digest>   → delete manifest
//
// The base host is normalized to include scheme; tests pass an httptest URL,
// production code passes "registry.fly.io" and we prepend "https://".
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"aa/v2/dockerimage"
)

// httpRegistry is the production Registry. It is safe to share across goroutines.
type httpRegistry struct {
	base   string // full URL root, e.g. "https://registry.fly.io"
	token  string
	client *http.Client

	loginMu   sync.Mutex
	loggedIn  bool
}

// New returns a Registry wired to the given host and token.
// host may be a bare hostname ("registry.fly.io") or a full URL
// ("http://127.0.0.1:42123") — the latter form is used by tests.
//
// Example: registry.New("registry.fly.io", "fo1_abc").
func New(host, token string) Registry {
	base := host
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	return &httpRegistry{
		base:   base,
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Login is a no-op for the HTTP client: bearer-token auth is applied per
// request, there is no session to establish. It is idempotent by construction.
func (r *httpRegistry) Login(ctx context.Context) error {
	r.loginMu.Lock()
	r.loggedIn = true
	r.loginMu.Unlock()
	return nil
}

// Push is not performed over the HTTP API in this tool — pushes go through
// the `docker push` subprocess so the user's existing Docker daemon handles
// layer blob uploads. The Registry.Push contract is kept for interface
// symmetry; docker-images's cmd.go calls the runner directly and never routes
// a push through this method.
func (r *httpRegistry) Push(ctx context.Context, tag string) error {
	return fmt.Errorf("registry.Push: not supported — use docker runner for push")
}

// catalogResponse is the shape of GET /v2/_catalog.
type catalogResponse struct {
	Repositories []string `json:"repositories"`
}

// tagsResponse is the shape of GET /v2/<repo>/tags/list.
type tagsResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// List walks the catalog, then each repo's tags, filtering by prefix.
// A non-empty prefix is matched against the repo path (not the full tag URL),
// so prefix "aa-apps/" keeps "aa-apps/myapi".
func (r *httpRegistry) List(ctx context.Context, prefix string) ([]Image, error) {
	var cat catalogResponse
	if err := r.getJSON(ctx, "/v2/_catalog", &cat); err != nil {
		return nil, err
	}
	host := hostFromBase(r.base)
	out := []Image{}
	for _, repo := range cat.Repositories {
		if prefix != "" && !strings.HasPrefix(repo, prefix) {
			continue
		}
		var tr tagsResponse
		if err := r.getJSON(ctx, "/v2/"+repo+"/tags/list", &tr); err != nil {
			return nil, err
		}
		for _, tag := range tr.Tags {
			out = append(out, Image{
				Tag: fmt.Sprintf("%s/%s:%s", host, repo, tag),
			})
		}
	}
	return out, nil
}

// Delete resolves the manifest digest for the given tag and then issues the
// registry DELETE. The two-step dance is required by the Docker Registry v2
// protocol: you cannot delete by tag, only by digest.
func (r *httpRegistry) Delete(ctx context.Context, tag string) error {
	ref, err := dockerimage.ParseFullyQualified(tag)
	if err != nil {
		return fmt.Errorf("delete %s: %w", tag, err)
	}
	digest, err := r.resolveDigest(ctx, ref)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/v2/%s/manifests/%s", ref.Repo, digest)
	req, err := r.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s: %w", tag, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return r.statusError(resp, tag)
}

// resolveDigest looks up the content digest for an image reference. Registry
// v2 returns it in the Docker-Content-Digest header on HEAD /manifests/<ref>.
func (r *httpRegistry) resolveDigest(ctx context.Context, ref dockerimage.ImageRef) (string, error) {
	// If the reference already looks like a digest, use it directly.
	if strings.HasPrefix(ref.Reference, "sha256:") {
		return ref.Reference, nil
	}
	path := fmt.Sprintf("/v2/%s/manifests/%s", ref.Repo, ref.Reference)
	req, err := r.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve digest for %s: %w", ref.String(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", r.statusError(resp, ref.String())
	}
	if d := resp.Header.Get("Docker-Content-Digest"); d != "" {
		return d, nil
	}
	// Fallback: some registries only set the digest in the body — but for v1
	// of this tool we require the header. Be explicit.
	return "", fmt.Errorf("registry did not return Docker-Content-Digest for %s", ref.String())
}

// getJSON issues a GET against the given URL path and decodes the response.
func (r *httpRegistry) getJSON(ctx context.Context, path string, out any) error {
	req, err := r.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return r.statusError(resp, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// newRequest builds a request with the bearer token attached.
func (r *httpRegistry) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	return req, nil
}

// statusError maps a non-2xx response to a typed-ish error message that names
// the configured token on auth failures and the target on 404s.
func (r *httpRegistry) statusError(resp *http.Response, target string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	preview := strings.TrimSpace(string(body))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("registry rejected token (HTTP %d) on %s — check that token.flyio has registry write access", resp.StatusCode, target)
	case http.StatusNotFound:
		return fmt.Errorf("registry: not found: %s (HTTP 404)", target)
	case http.StatusTooManyRequests:
		retry := resp.Header.Get("Retry-After")
		if retry == "" {
			retry = "later"
		}
		return fmt.Errorf("registry rate-limited on %s — retry in %s", target, retry)
	}
	if preview != "" {
		return fmt.Errorf("registry: %s failed: HTTP %d: %s", target, resp.StatusCode, preview)
	}
	return fmt.Errorf("registry: %s failed: HTTP %d", target, resp.StatusCode)
}

// hostFromBase strips the scheme from a base URL so it can be composed into
// a printable "host/repo:tag" tag string.
func hostFromBase(base string) string {
	h := base
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimRight(h, "/")
	return h
}
