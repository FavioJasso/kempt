package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFixturesUpToDate rebuilds every fixture twice and compares both with
// the committed file, so nondeterminism and stale fixtures both fail.
func TestFixturesUpToDate(t *testing.T) {
	for _, fx := range fixtures {
		a, err := fx.build()
		if err != nil {
			t.Fatalf("%s: %v", fx.name, err)
		}
		b, err := fx.build()
		if err != nil {
			t.Fatalf("%s: %v", fx.name, err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two builds differ (sha256 %x vs %x)", fx.name, sha256.Sum256(a), sha256.Sum256(b))
		}
		committed, err := os.ReadFile(filepath.Join("..", "fixtures", fx.name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, committed) {
			t.Errorf("%s: committed fixture is stale (sha256 %x, generator makes %x); run go run ./testdata/gen",
				fx.name, sha256.Sum256(committed), sha256.Sum256(a))
		}
	}
}

// TestOutFlag runs the command into a temporary directory.
func TestOutFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command")
	}
	dir := t.TempDir()
	out, err := exec.Command("go", "run", ".", "-out", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	for _, fx := range fixtures {
		got, err := os.ReadFile(filepath.Join(dir, fx.name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join("..", "fixtures", fx.name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: -out output differs from the committed fixture", fx.name)
		}
	}
}
