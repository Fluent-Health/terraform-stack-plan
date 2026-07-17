package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Fluent-Health/terraform-stack-plan/internal/config"
	"github.com/Fluent-Health/terraform-stack-plan/internal/runner"
)

// runWhoami executes the whoami subcommand, printing the server, audience,
// and resolved authenticated Google OIDC identity.
func runWhoami(args []string) int {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	env := fs.String("env", "", "environment name (optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// 1. Get base server URL and audience
	base := os.Getenv(runner.EnvServer)
	if base == "" {
		// Discover from config
		if p, ok := config.Discover("."); ok {
			if cfg, err := config.Load(p); err == nil {
				if *env != "" {
					for _, s := range cfg.Servers {
						if s.Environment == *env || s.Name == *env {
							base = s.URL
							break
						}
					}
				}
				if base == "" && cfg.Server != nil {
					base = cfg.Server.URL
				}
			}
		}
	}

	aud := os.Getenv(runner.EnvAudience)
	if aud == "" {
		aud = base
	}

	fmt.Printf("Server URL: %s\n", base)
	fmt.Printf("OIDC Audience: %s\n", aud)

	// 2. Fetch/resolve identity
	if aud == "" {
		fmt.Println("Identity: anonymous (no server/audience configured)")
		return 0
	}

	tokFunc, err := runner.APITokenFunc(aud)
	if err != nil {
		fmt.Printf("Identity: Lookup failed — Google ADC is unavailable: %v\n", err)
		return 1
	}

	tok, err := tokFunc(context.Background())
	if err != nil {
		fmt.Printf("Identity: Fetching token failed: %v\n", err)
		return 1
	}

	id, err := decodeWhoamiJWTSubject(tok)
	if err != nil {
		fmt.Printf("Identity: Fetched token, but payload decode failed: %v\n", err)
		return 1
	}

	fmt.Printf("Identity: %s\n", id)
	return 0
}

func decodeWhoamiJWTSubject(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if claims.Email != "" {
		return claims.Email, nil
	}
	return claims.Sub, nil
}
