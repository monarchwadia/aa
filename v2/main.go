package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	flyAPIBase        = "https://api.machines.dev/v1"
	configKeyFlyToken = "token.flyio"
	defaultApp        = "aa-apps"
)

// --- config store ---

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aa", "config"), nil
}

func readConfig() (map[string]string, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			cfg[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return cfg, nil
}

func writeConfig(cfg map[string]string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	var sb strings.Builder
	for k, v := range cfg {
		fmt.Fprintf(&sb, "%s=%s\n", k, v)
	}
	return os.WriteFile(path, []byte(sb.String()), 0600)
}

func runConfig(args []string) {
	if len(args) == 0 {
		cfg, err := readConfig()
		if err != nil {
			log.Fatalf("read config: %v", err)
		}
		if len(cfg) == 0 {
			fmt.Println("(no config set)")
			return
		}
		for k, v := range cfg {
			fmt.Printf("%s=%s\n", k, v)
		}
		return
	}

	cfg, err := readConfig()
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			log.Fatalf("invalid config argument %q — expected key=value", arg)
		}
		cfg[strings.TrimSpace(k)] = strings.TrimSpace(v)
		fmt.Printf("saved %s\n", k)
	}
	if err := writeConfig(cfg); err != nil {
		log.Fatalf("write config: %v", err)
	}
}

// --- fly machine ---

type createMachineRequest struct {
	Config machineConfig `json:"config"`
	Region string        `json:"region,omitempty"`
}

type machineConfig struct {
	Image string      `json:"image"`
	Init  *initConfig `json:"init,omitempty"`
}

type initConfig struct {
	Exec []string `json:"exec,omitempty"`
}

type machine struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Region string `json:"region"`
}

func runSpawn(args []string) {
	fs := flag.NewFlagSet("spawn", flag.ExitOnError)
	token := fs.String("token", "", "Fly.io API token (overrides config and FLY_API_TOKEN)")
	app := fs.String("app", "", "Fly.io app name (defaults to "+defaultApp+")")
	image := fs.String("image", "ubuntu:22.04", "Docker image for the machine")
	region := fs.String("region", "", "Fly.io region (optional, e.g. iad)")
	fs.Parse(args)

	if err := preflight(); err != nil {
		log.Fatal(err)
	}

	// resolve token: flag > env > config file
	tok := *token
	if tok == "" {
		tok = os.Getenv("FLY_API_TOKEN")
	}
	if tok == "" {
		cfg, err := readConfig()
		if err != nil {
			log.Fatalf("read config: %v", err)
		}
		tok = cfg[configKeyFlyToken]
	}
	if tok == "" {
		log.Fatalf("no Fly.io token found — run: aa config %s=<token>", configKeyFlyToken)
	}

	appName := *app
	if appName == "" {
		appName = defaultApp
		fmt.Printf("No app specified, using %q...\n", appName)
		if err := ensureApp(tok, appName); err != nil {
			log.Fatalf("ensure app: %v", err)
		}
	}

	fmt.Printf("Creating machine in app %q (image: %s)...\n", appName, *image)
	m, err := createMachine(tok, appName, *image, *region)
	if err != nil {
		log.Fatalf("create machine: %v", err)
	}
	fmt.Printf("Machine %s created (region: %s), waiting to start...\n", m.ID, m.Region)

	if err := waitForState(tok, appName, m.ID, "started", 90*time.Second); err != nil {
		log.Fatalf("wait: %v", err)
	}
	fmt.Printf("Machine %s is running — waiting for SSH...\n", m.ID)

	if err := attachSSH(tok, appName, m.ID); err != nil {
		log.Fatalf("ssh console: %v", err)
	}
}

func attachSSH(token, appName, machineID string) error {
	const maxAttempts = 15
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command("flyctl", "ssh", "console", "--app", appName, "--machine", machineID)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), "FLY_API_TOKEN="+token)
		err := cmd.Run()
		if err == nil {
			return nil
		}
		if attempt == maxAttempts {
			return err
		}
		fmt.Printf("  SSH not ready yet (attempt %d/%d), retrying in 3s...\n", attempt, maxAttempts)
		time.Sleep(3 * time.Second)
	}
	return nil
}

func ensureApp(token, appName string) error {
	// check if app exists
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/apps/%s", flyAPIBase, appName), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 200 {
		return nil // already exists
	}
	if resp.StatusCode != 404 {
		return fmt.Errorf("checking app existence: HTTP %d", resp.StatusCode)
	}

	// create the app
	fmt.Printf("App %q not found, creating it...\n", appName)
	body, _ := json.Marshal(map[string]string{"app_name": appName, "org_slug": "personal"})
	req, err = http.NewRequest("POST", fmt.Sprintf("%s/apps", flyAPIBase), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create app HTTP %d: %s", resp.StatusCode, b)
	}
	fmt.Printf("App %q created.\n", appName)
	return nil
}

func createMachine(token, app, image, region string) (*machine, error) {
	body := createMachineRequest{
		Config: machineConfig{
			Image: image,
			Init:  &initConfig{Exec: []string{"/bin/sleep", "infinity"}},
		},
		Region: region,
	}
	data, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/apps/%s/machines", flyAPIBase, app), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}

	var m machine
	return &m, json.NewDecoder(resp.Body).Decode(&m)
}

func waitForState(token, app, machineID, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m, err := getMachine(token, app, machineID)
		if err != nil {
			return err
		}
		if m.State == want {
			return nil
		}
		fmt.Printf("  state: %s\n", m.State)
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out after %s waiting for state %q", timeout, want)
}

func getMachine(token, app, machineID string) (*machine, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/apps/%s/machines/%s", flyAPIBase, app, machineID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}

	var m machine
	return &m, json.NewDecoder(resp.Body).Decode(&m)
}

// --- main ---

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, `usage: aa <command> [args]

commands:
  config [key=value ...]   get or set config values
  spawn  [--app <name>]    create a machine and attach
  ls     [--app <name>]    list machines
  start  <id> [<id>...]    start stopped machines
  stop   <id> [<id>...]    stop running machines
  rm     [--force] <id>... destroy machines
`)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "config":
		runConfig(os.Args[2:])
	case "spawn":
		runSpawn(os.Args[2:])
	case "ls":
		runList(os.Args[2:])
	case "start":
		runStart(os.Args[2:])
	case "stop":
		runStop(os.Args[2:])
	case "rm":
		runRm(os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}
