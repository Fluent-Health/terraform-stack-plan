package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseWatches(t *testing.T) {
	tmp := t.TempDir()
	content := `
	stack {
		name  = "test"
		id    = "test-id"
		watch = [
			"../../components/am",
			"../../components/apim"
		]
	}
	`
	fp := filepath.Join(tmp, "stack.tm.hcl")
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := ParseWatches(fp)
	if err != nil {
		t.Fatalf("ParseWatches: %v", err)
	}
	if len(w) != 2 || w[0] != "../../components/am" || w[1] != "../../components/apim" {
		t.Fatalf("unexpected watches: %v", w)
	}
}
