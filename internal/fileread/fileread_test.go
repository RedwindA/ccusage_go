package fileread

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWithReusesBuffersSafely(t *testing.T) {
	dir := t.TempDir()
	contents := [][]byte{bytes.Repeat([]byte("a"), 100000), []byte("short"), {}, bytes.Repeat([]byte("xyz\n"), 5000)}
	for round := 0; round < 3; round++ {
		for i, want := range contents {
			path := filepath.Join(dir, string(rune('a'+i)))
			if err := os.WriteFile(path, want, 0o600); err != nil {
				t.Fatal(err)
			}
			data, release, err := Read(path)
			if err != nil {
				t.Fatal(err)
			}
			got := append([]byte(nil), data...)
			release()
			if !bytes.Equal(got, want) {
				t.Fatalf("round %d file %d: got %d bytes, want %d", round, i, len(got), len(want))
			}
		}
	}
	if _, _, err := Read(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing file: got %v", err)
	}
}
