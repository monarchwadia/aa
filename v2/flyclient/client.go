// client.go implements flyclient.Client against the real Fly Machines API.
// The body speaks the HTTP shape documented at https://fly.io/docs/machines/api-machines-resource/.
// All requests are authenticated with a bearer token; all responses are decoded
// into the internal flyclient types that callers consume.
package flyclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpClient is the concrete Client implementation. It is constructed via New
// and never exported — callers always hold the Client interface.
type httpClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a Client pointed at baseURL and authenticated with token.
// baseURL is the Fly Machines API root, e.g. https://api.machines.dev/v1;
// token is a Fly org-scoped API token.
//
// Example:
//
//	c := flyclient.New("https://api.machines.dev/v1", "fo1_abc123")
//	m, err := c.Create(ctx, "aa-apps", flyclient.SpawnSpec{Image: "ubuntu:22.04"})
func New(baseURL, token string) Client {
	return &httpClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{},
	}
}

// do builds, sends, and reads one request end-to-end. It returns the response
// body bytes, the status code, and a setup error (network, context). It does
// NOT translate non-2xx to an error — the caller decides per endpoint whether
// 404/409/etc are sentinel-worthy.
func (c *httpClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("flyclient: marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("flyclient: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, buf, nil
}

// statusError maps non-2xx HTTP responses to typed errors that callers can
// pattern-match via errors.Is.
func statusError(method, path string, status int, body []byte) error {
	msg := fmt.Sprintf("flyclient: %s %s: HTTP %d: %s", method, path, status, string(body))
	switch status {
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w", msg, ErrNotFound)
	case http.StatusConflict:
		return fmt.Errorf("%s: %w", msg, ErrConflict)
	}
	return fmt.Errorf("%s", msg)
}

// flyMachine is the on-the-wire shape Fly returns for a machine. It holds
// a nested config.metadata map that carries labels written via SpawnSpec.Labels.
type flyMachine struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Region string `json:"region"`
	Config struct {
		Metadata map[string]string `json:"metadata,omitempty"`
	} `json:"config,omitempty"`
}

func (m flyMachine) toMachine() Machine {
	return Machine{
		ID:     m.ID,
		State:  m.State,
		Region: m.Region,
		Labels: m.Config.Metadata,
	}
}

// createMachineBody is the POST /apps/:app/machines payload. Only the fields
// aa needs are populated; everything else relies on Fly's server-side defaults.
type createMachineBody struct {
	Config createMachineConfig `json:"config"`
	Region string              `json:"region,omitempty"`
}

type createMachineConfig struct {
	Image    string            `json:"image"`
	Init     *createMachineInit `json:"init,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type createMachineInit struct {
	Exec []string `json:"exec,omitempty"`
}

// EnsureApp GETs /apps/:app; if the app is missing (404), it POSTs /apps with
// the default "personal" org slug. Any other non-2xx is surfaced as an error.
func (c *httpClient) EnsureApp(ctx context.Context, appName string) error {
	path := "/apps/" + appName
	status, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	if status != http.StatusNotFound {
		return statusError(http.MethodGet, path, status, body)
	}
	createBody := map[string]string{"app_name": appName, "org_slug": "personal"}
	createStatus, createResp, err := c.do(ctx, http.MethodPost, "/apps", createBody)
	if err != nil {
		return err
	}
	if createStatus >= 300 {
		return statusError(http.MethodPost, "/apps", createStatus, createResp)
	}
	return nil
}

// Create POSTs /apps/:app/machines with the spec and returns the resulting machine.
func (c *httpClient) Create(ctx context.Context, appName string, spec SpawnSpec) (Machine, error) {
	path := "/apps/" + appName + "/machines"
	body := createMachineBody{
		Config: createMachineConfig{
			Image:    spec.Image,
			Init:     &createMachineInit{Exec: []string{"/bin/sleep", "infinity"}},
			Metadata: spec.Labels,
		},
		Region: spec.Region,
	}
	status, resp, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return Machine{}, err
	}
	if status >= 300 {
		return Machine{}, statusError(http.MethodPost, path, status, resp)
	}
	var m flyMachine
	if err := json.Unmarshal(resp, &m); err != nil {
		return Machine{}, fmt.Errorf("flyclient: decode create response: %w", err)
	}
	return m.toMachine(), nil
}

// Get returns a single machine by ID; 404 surfaces as ErrNotFound.
func (c *httpClient) Get(ctx context.Context, appName, machineID string) (Machine, error) {
	path := "/apps/" + appName + "/machines/" + machineID
	status, resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Machine{}, err
	}
	if status >= 300 {
		return Machine{}, statusError(http.MethodGet, path, status, resp)
	}
	var m flyMachine
	if err := json.Unmarshal(resp, &m); err != nil {
		return Machine{}, fmt.Errorf("flyclient: decode get response: %w", err)
	}
	return m.toMachine(), nil
}

// WaitStarted polls GET /machines/:id every 2s until state=="started" or
// the supplied context expires. It does not enforce its own deadline — the
// caller layers BackendDeadline() via ctx.
func (c *httpClient) WaitStarted(ctx context.Context, appName, machineID string) error {
	for {
		m, err := c.Get(ctx, appName, machineID)
		if err != nil {
			return err
		}
		if m.State == "started" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// List returns every machine in appName.
func (c *httpClient) List(ctx context.Context, appName string) ([]Machine, error) {
	path := "/apps/" + appName + "/machines"
	status, resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, statusError(http.MethodGet, path, status, resp)
	}
	var ms []flyMachine
	if err := json.Unmarshal(resp, &ms); err != nil {
		return nil, fmt.Errorf("flyclient: decode list response: %w", err)
	}
	out := make([]Machine, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.toMachine())
	}
	return out, nil
}

// Start POSTs /apps/:app/machines/:id/start.
func (c *httpClient) Start(ctx context.Context, appName, machineID string) error {
	return c.postAction(ctx, appName, machineID, "start")
}

// Stop POSTs /apps/:app/machines/:id/stop.
func (c *httpClient) Stop(ctx context.Context, appName, machineID string) error {
	return c.postAction(ctx, appName, machineID, "stop")
}

func (c *httpClient) postAction(ctx context.Context, appName, machineID, action string) error {
	path := "/apps/" + appName + "/machines/" + machineID + "/" + action
	status, resp, err := c.do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return statusError(http.MethodPost, path, status, resp)
	}
	return nil
}

// Destroy DELETEs /apps/:app/machines/:id, appending ?force=true when requested.
func (c *httpClient) Destroy(ctx context.Context, appName, machineID string, force bool) error {
	path := "/apps/" + appName + "/machines/" + machineID
	if force {
		path += "?force=true"
	}
	status, resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return statusError(http.MethodDelete, path, status, resp)
	}
	return nil
}

// FindByLabel lists every machine in appName and returns those whose Labels
// map contains an exact key=value entry. Zero matches returns (nil, nil).
func (c *httpClient) FindByLabel(ctx context.Context, appName, key, value string) ([]Machine, error) {
	all, err := c.List(ctx, appName)
	if err != nil {
		return nil, err
	}
	var out []Machine
	for _, m := range all {
		if m.Labels[key] == value {
			out = append(out, m)
		}
	}
	return out, nil
}
