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
	"github.com/Fluent-Health/terraform-stack-plan/internal/jwtutil"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
	"github.com/Fluent-Health/terraform-stack-plan/internal/store"
)

type cliExecution struct {
	store.Execution
	Graph events.Graph       `json:"graph"`
	Gates []store.GateTarget `json:"gates"`
}

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

	// Execute initial fetch
	exec, err := fetchExecution(srv, tok, execID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run status: fetch failed: %v\n", err)
		return 1
	}

	if *watch && isExecutionTerminal(exec.Status) {
		printExecution(exec, *format)
		return exitCode(exec.Status)
	}

	if !*watch {
		printExecution(exec, *format)
		return exitCode(exec.Status)
	}

	// Watch Mode
	printExecution(exec, *format)
	if err := watchExecution(srv, tok, execID, *format); err != nil {
		fmt.Fprintf(os.Stderr, "run status watch: %v\n", err)
		return 1
	}
	return 0
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

func makeJWT(secret string) string {
	t, _ := jwtutil.Make(secret, "runner", "api", time.Hour)
	return t
}

func fetchExecution(srv, tok, id string) (cliExecution, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/execution/%s", srv, id), nil)
	if err != nil {
		return cliExecution{}, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+makeJWT(tok))
	}
	resp, err := http.DefaultClient.Do(req)
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

func printExecution(exec cliExecution, format string) {
	if format == "json" {
		b, _ := json.MarshalIndent(exec, "", "  ")
		fmt.Println(string(b))
		return
	}
	// Clear screen if watch mode and TTY (not clearing here for unit tests)
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

func watchExecution(srv, tok, id, format string) error {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/execution/%s/events", srv, id), nil)
	if err != nil {
		return err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+makeJWT(tok))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events stream http %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: changed") {
			exec, err := fetchExecution(srv, tok, id)
			if err == nil {
				printExecution(exec, format)
				if isExecutionTerminal(exec.Status) {
					return nil
				}
			}
		}
	}
	return scanner.Err()
}
