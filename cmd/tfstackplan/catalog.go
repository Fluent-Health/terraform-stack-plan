package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Fluent-Health/terraform-stack-plan/internal/catalog"
)

func runCatalog(args []string) int {
	fs := flag.NewFlagSet("catalog build", flag.ContinueOnError)
	dir := fs.String("dir", ".", "terramate project root (required)")
	outPath := fs.String("o", "", "output file path (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cat, err := catalog.Build(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog build:", err)
		return 1
	}

	data, jerr := json.MarshalIndent(cat, "", "  ")
	if jerr != nil {
		fmt.Fprintln(os.Stderr, "catalog build: marshal:", jerr)
		return 1
	}

	if *outPath == "" || *outPath == "-" {
		fmt.Println(string(data))
	} else {
		if werr := os.WriteFile(*outPath, data, 0644); werr != nil {
			fmt.Fprintln(os.Stderr, "catalog build: write file:", werr)
			return 1
		}
	}
	return 0
}
