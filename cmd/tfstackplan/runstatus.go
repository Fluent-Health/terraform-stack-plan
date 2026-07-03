package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Fluent-Health/terraform-stack-plan/internal/events"
	"github.com/Fluent-Health/terraform-stack-plan/internal/gauth"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

type cliExecution struct {
	store.Execution
	Graph events.Graph       `json:"graph"`
	Gates []store.GateTarget `json:"gates"`
}

var statusClient = &http.Client{Timeout: 10 * time.Second}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("run status", flag.ContinueOnError)
	serverURL := fs.String("server", "", "server base URL (defaults to $"+runner.EnvServer+")")
	token := fs.String("token", "", "bearer token (defaults to $"+runner.EnvToken+")")
	format := fs.String("format", "text", "output format: text|json")
	watch := fs.Bool("watch", false, "block and watch for real-time status updates")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	execID := fs.Arg(0)
	if execID == "" {
		execID = os.Getenv(runner.EnvExecution)
	}
	if execID == "" {
		fmt.Fprintln(os.Stderr, "run status: execution ID is required as an argument or via $"+runner.EnvExecution)
		return 2
	}
	srv := *serverURL
	if srv == "" {
		srv = os.Getenv(runner.EnvServer)
	}
	tok := *token
	if tok == "" {
		tok = os.Getenv(runner.EnvToken)
	}
	if srv == "" {
		fmt.Fprintln(os.Stderr, "run status: server URL is required")
		return 2
	}
	srv = strings.TrimRight(srv, "/")
	bearer := apiBearer(tok)

	// Execute initial fetch
	exec, err := fetchExecution(srv, bearer, execID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run status: fetch failed: %v\n", err)
		return 1
	}

	if *watch && isExecutionTerminal(exec.Status) {
		printExecution(exec, *format, false)
		return exitCode(exec.Status)
	}

	if !*watch {
		printExecution(exec, *format, false)
		return exitCode(exec.Status)
	}

	// Watch Mode
	printExecution(exec, *format, false)
	finalExec, err := watchExecution(srv, bearer, execID, *format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run status watch: %v\n", err)
		return 1
	}
	return exitCode(finalExec.Status)
}

func isExecutionTerminal(status string) bool {
	return status == "success" || status == "failure"
}

func exitCode(status string) int {
	if status == "failure" {
		return 1
	}
	return 0
}

// apiBearer returns the bearer-token source for /api/* calls — the same
// credential selection as the runner client (runner.APITokenFunc): the shared
// secret when tok is set (legacy HS256), else Google OIDC via ADC when
// $TFSTACKPLAN_AUDIENCE is set, else nil (unauthenticated). An unavailable ADC
// is warned about rather than silently degraded.
func apiBearer(tok string) gauth.TokenFunc {
	src, err := runner.APITokenFunc(tok, os.Getenv(runner.EnvAudience))
	if err != nil {
		fmt.Fprintf(os.Stderr, "run status: %s is set but Google ADC is unavailable (%v) — requests will be unauthenticated\n", runner.EnvAudience, err)
	}
	return src
}

// setBearer attaches the bearer token to req (a no-op for a nil provider).
func setBearer(req *http.Request, bearer gauth.TokenFunc) error {
	if bearer == nil {
		return nil
	}
	tok, err := bearer(req.Context())
	if err != nil {
		return fmt.Errorf("api token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

func fetchExecution(srv string, bearer gauth.TokenFunc, id string) (cliExecution, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/execution/%s", srv, id), nil)
	if err != nil {
		return cliExecution{}, err
	}
	if err := setBearer(req, bearer); err != nil {
		return cliExecution{}, err
	}
	resp, err := statusClient.Do(req)
	if err != nil {
		return cliExecution{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cliExecution{}, fmt.Errorf("http %d", resp.StatusCode)
	}
	var res cliExecution
	err = json.NewDecoder(resp.Body).Decode(&res)
	return res, err
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func printExecution(exec cliExecution, format string, clear bool) {
	if format == "json" {
		b, _ := json.MarshalIndent(exec, "", "  ")
		fmt.Println(string(b))
		return
	}
	if clear && isTTY() {
		fmt.Print("\033[H\033[2J")
	}
	fmt.Printf("Execution ID: %s\n", exec.ID)
	fmt.Printf("Repo/PR:      %s #%d\n", exec.Repo, exec.PR)
	fmt.Printf("Status:       %s\n", strings.ToUpper(exec.Status))
	fmt.Printf("Phase:        %s\n", exec.Phase)
	fmt.Printf("Environment:  %s\n\n", exec.Environment)

	fmt.Println("Stacks:")
	for _, s := range exec.Graph.Stacks {
		changeStr := "No Changes"
		if s.Counts != nil {
			changeStr = fmt.Sprintf("+%d, -%d, ~%d", s.Counts.Add, s.Counts.Destroy, s.Counts.Change)
		}
		fmt.Printf("  %-30s: %-12s | %s\n", s.Path, strings.ToUpper(string(s.Status)), changeStr)
	}
	if len(exec.Gates) > 0 {
		fmt.Println("\nGates:")
		for _, g := range exec.Gates {
			fmt.Printf("  %s (%s): %s\n", g.Class, g.Target, g.State)
		}
	}
}

func watchExecution(srv string, bearer gauth.TokenFunc, id, format string) (cliExecution, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/execution/%s/events", srv, id), nil)
	if err != nil {
		return cliExecution{}, err
	}
	if err := setBearer(req, bearer); err != nil {
		return cliExecution{}, err
	}
	// events stream stays open, so do NOT use statusClient's 10s timeout
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return cliExecution{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cliExecution{}, fmt.Errorf("events stream http %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: changed") {
			exec, err := fetchExecution(srv, bearer, id)
			if err == nil {
				printExecution(exec, format, true)
				if isExecutionTerminal(exec.Status) {
					return exec, nil
				}
			}
		}
	}
	if scanner.Err() != nil {
		return cliExecution{}, scanner.Err()
	}

	// Scanner finished; do a final fetch to get the absolute current state
	finalExec, err := fetchExecution(srv, bearer, id)
	if err != nil {
		return finalExec, err
	}
	if !isExecutionTerminal(finalExec.Status) {
		return finalExec, fmt.Errorf("events stream ended prematurely while execution was %s", finalExec.Status)
	}
	return finalExec, nil
}
