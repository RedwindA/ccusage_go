package dirwalk

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A symlinked root is walked under its own name; symlinks below it are
// reported, not followed.
func TestWalkFollowsOnlyASymlinkedRoot(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	for _, name := range []string{"store/a/s.jsonl", "other/b/s.jsonl"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir, "projects")
	if err := os.Symlink(store, root); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(filepath.Join(dir, "other/b"), filepath.Join(store, "b")); err != nil {
		t.Fatal(err)
	}
	var got []string
	err := Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		got = append(got, path+map[bool]string{true: " ->"}[d.Type()&fs.ModeSymlink != 0])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{root, filepath.Join(root, "a"), filepath.Join(root, "a/s.jsonl"), filepath.Join(root, "b") + " ->"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walked %q, want %q", got, want)
	}
}
