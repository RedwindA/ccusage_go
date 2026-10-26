// Package dirwalk walks a usage source's directory tree as upstream ccusage
// lists it.
package dirwalk

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Walk is filepath.WalkDir, except that a root that links to a directory,
// such as a projects directory moved to another disk and left behind as a
// symlink or a Windows junction, is followed, as upstream's read_dir follows
// it. fn still sees paths under root, so names taken from them do not
// change, and links below root are not followed.
func Walk(root string, fn fs.WalkDirFunc) error {
	if info, err := os.Lstat(root); err != nil || info.IsDir() {
		return filepath.WalkDir(root, fn)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return filepath.WalkDir(root, fn)
	}
	// Lstat, which WalkDir starts from, follows a link named with a
	// trailing separator, a junction included, which filepath.EvalSymlinks
	// leaves alone.
	dir := root + string(filepath.Separator)
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if path == dir {
			path = root
		}
		return fn(path, d, err)
	})
}

// IsFile reports whether a walked entry of type t is a file upstream lists:
// a regular file, or an irregular one, which is how Go reports a Windows
// reparse point other than a symlink, such as a OneDrive Files On-Demand
// placeholder that Rust's FileType::is_file reads as a file. A symlink is
// not a file.
func IsFile(t fs.FileMode) bool {
	return t.IsRegular() || t == fs.ModeIrregular
}
