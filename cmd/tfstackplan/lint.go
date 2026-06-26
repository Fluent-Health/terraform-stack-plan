package main

import (
	"flag"
	"fmt"
	"os"
)

func runLint(args []string) int {
	fs := flag.NewFlagSet("run lint", flag.ContinueOnError)
	dir := fs.String("dir", "", "terramate project root (required)")
	_ = fs.Bool("changed", true, "only lint changed stacks")
	_ = fs.Int("parallel", 0, "parallel lint jobs (0 = terramate default)")
	_ = fs.String("base", "", "git base ref for change detection")
	_ = fs.String("script", "lint", "terramate script name to run")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "tfstackplan run lint: --dir is required")
		return 2
	}
	return 0
}
