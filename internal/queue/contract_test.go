package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readContractFile(t *testing.T, name string) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}

	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "contracts", name)
		if data, err := os.ReadFile(candidate); err == nil {
			return strings.TrimSpace(string(data))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("contract file not found: %s", name)
	return ""
}

func TestSpecHashContract(t *testing.T) {
	canonical := readContractFile(t, "spec_hash.json")
	expected := readContractFile(t, "spec_hash.sha256")

	sum := sha256.Sum256([]byte(canonical))
	got := hex.EncodeToString(sum[:])

	if got != expected {
		t.Fatalf("spec hash contract mismatch: got %s want %s", got, expected)
	}
}
