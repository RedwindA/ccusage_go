package dirwalk

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// A junction, the usual way to move a directory on Windows without admin
// rights, is followed like a symlinked root, though Lstat reports it as
// irregular and filepath.EvalSymlinks leaves it alone.
func TestWalkFollowsAJunctionRoot(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	if err := os.MkdirAll(filepath.Join(store, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "a", "s.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "projects")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", root, store).CombinedOutput(); err != nil {
		t.Skip(err, string(out))
	}
	var got []string
	err := Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		got = append(got, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{root, filepath.Join(root, "a"), filepath.Join(root, "a", "s.jsonl")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walked %q, want %q", got, want)
	}
}
