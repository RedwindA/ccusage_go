// Package dirwalk walks a usage source's directory tree as upstream ccusage
// lists it.
package dirwalk

import (
	"io/fs"
	"path/filepath"
)

// Walk is filepath.WalkDir, except that a root that is a symlink, such as a
// projects directory moved to another disk, is followed, as upstream's
// read_dir follows it. fn still sees paths under root, so names taken from
// them do not change, and symlinks below root are not followed.
func Walk(root string, fn fs.WalkDirFunc) error {
	target, err := filepath.EvalSymlinks(root)
	if err != nil || target == root {
		return filepath.WalkDir(root, fn)
	}
	return filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(target, path)
		return fn(filepath.Join(root, rel), d, err)
	})
}
