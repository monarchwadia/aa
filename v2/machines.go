package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"text/tabwriter"
)

// registerCommonFlags adds --token and --app to a FlagSet. Call resolveCommon after fs.Parse.
func registerCommonFlags(fs *flag.FlagSet) (*string, *string) {
	tokenFlag := fs.String("token", "", "Fly.io API token")
	appFlag := fs.String("app", "", "Fly.io app name (defaults to "+defaultApp+")")
	return tokenFlag, appFlag
}

func resolveCommon(tokenFlag, appFlag *string) (token, app string) {
	tok := *tokenFlag
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
	a := *appFlag
	if a == "" {
		a = defaultApp
	}
	return tok, a
}

func runList(args []string) {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	tokenFlag, appFlag := registerCommonFlags(fs)
	fs.Parse(args)
	tok, app := resolveCommon(tokenFlag, appFlag)

	machines, err := listMachines(tok, app)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	if len(machines) == 0 {
		fmt.Printf("(no machines in %q)\n", app)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tREGION")
	for _, m := range machines {
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.ID, m.State, m.Region)
	}
	w.Flush()
}

func runStart(args []string) {
	runLifecycle("start", args, nil, func(tok, app, id string, _ map[string]any) error {
		return postAction(tok, app, id, "start")
	})
}

func runStop(args []string) {
	runLifecycle("stop", args, nil, func(tok, app, id string, _ map[string]any) error {
		return postAction(tok, app, id, "stop")
	})
}

func runRm(args []string) {
	setup := func(fs *flag.FlagSet) map[string]any {
		force := fs.Bool("force", false, "force destroy even if running")
		return map[string]any{"force": force}
	}
	runLifecycle("rm", args, setup, func(tok, app, id string, extra map[string]any) error {
		force := *(extra["force"].(*bool))
		return destroyMachine(tok, app, id, force)
	})
}

func runLifecycle(verb string, args []string, setup func(*flag.FlagSet) map[string]any, fn func(tok, app, id string, extra map[string]any) error) {
	fs := flag.NewFlagSet(verb, flag.ExitOnError)
	tokenFlag, appFlag := registerCommonFlags(fs)
	var extra map[string]any
	if setup != nil {
		extra = setup(fs)
	}
	if err := fs.Parse(args); err != nil {
		log.Fatal(err)
	}
	ids := fs.Args()
	if len(ids) == 0 {
		log.Fatalf("%s: at least one machine ID required", verb)
	}
	tok, app := resolveCommon(tokenFlag, appFlag)
	for _, id := range ids {
		if err := fn(tok, app, id, extra); err != nil {
			log.Fatalf("%s %s: %v", verb, id, err)
		}
		fmt.Printf("%s %s ok\n", verb, id)
	}
}

// --- API calls ---

func listMachines(token, app string) ([]machine, error) {
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/apps/%s/machines", flyAPIBase, app), nil)
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
	var ms []machine
	return ms, json.NewDecoder(resp.Body).Decode(&ms)
}

func postAction(token, app, id, action string) error {
	url := fmt.Sprintf("%s/apps/%s/machines/%s/%s", flyAPIBase, app, id, action)
	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func destroyMachine(token, app, id string, force bool) error {
	url := fmt.Sprintf("%s/apps/%s/machines/%s", flyAPIBase, app, id)
	if force {
		url += "?force=true"
	}
	req, _ := http.NewRequest("DELETE", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}
