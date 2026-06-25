package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseLockFile(t *testing.T) {
	src := `
provider "registry.terraform.io/hashicorp/google" {
  version     = "6.10.0"
  constraints = ">= 5.0.0, < 7.0.0"
  hashes = [
    "h1:dummy",
  ]
}
`
	providers, err := parseLockFileContent([]byte(src))
	if err != nil {
		t.Fatalf("failed to parse lock file: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	p := providers[0]
	if p.Address != "registry.terraform.io/hashicorp/google" || p.Version != "6.10.0" {
		t.Errorf("unexpected parsed values: %+v", p)
	}
}

func TestParseLockFile_Multiple(t *testing.T) {
	src := `
provider "registry.terraform.io/hashicorp/google" {
  version     = "6.10.0"
  constraints = ">= 5.0.0, < 7.0.0"
  hashes = [
    "h1:dummy1",
  ]
}

provider "registry.terraform.io/hashicorp/aws" {
  version     = "5.32.0"
  hashes = [
    "h1:dummy2",
  ]
}
`
	providers, err := parseLockFileContent([]byte(src))
	if err != nil {
		t.Fatalf("failed to parse lock file: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}

	expected := map[string]string{
		"registry.terraform.io/hashicorp/google": "6.10.0",
		"registry.terraform.io/hashicorp/aws":    "5.32.0",
	}

	for _, p := range providers {
		v, ok := expected[p.Address]
		if !ok {
			t.Errorf("unexpected provider address: %s", p.Address)
			continue
		}
		if p.Version != v {
			t.Errorf("for provider %s expected version %s, got %s", p.Address, v, p.Version)
		}
	}
}

func TestParseLockFile_InvalidHCL(t *testing.T) {
	src := `
provider "registry.terraform.io/hashicorp/google" {
  version = 
`
	_, err := parseLockFileContent([]byte(src))
	if err == nil {
		t.Fatal("expected error parsing invalid HCL, got nil")
	}
}

func TestParseLockFile_MissingFile(t *testing.T) {
	_, err := ParseLockFile("nonexistent-file.hcl")
	if err == nil {
		t.Fatal("expected error reading non-existent file, got nil")
	}
}

func TestParseLockFile_RealFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".terraform.lock.hcl")

	src := `
provider "registry.terraform.io/hashicorp/google" {
  version     = "6.10.0"
  constraints = ">= 5.0.0, < 7.0.0"
  hashes = [
    "h1:dummy",
  ]
}
`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatalf("failed to write temp lock file: %v", err)
	}

	providers, err := ParseLockFile(path)
	if err != nil {
		t.Fatalf("failed to parse real lock file: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	p := providers[0]
	if p.Address != "registry.terraform.io/hashicorp/google" || p.Version != "6.10.0" {
		t.Errorf("unexpected parsed values: %+v", p)
	}
}
