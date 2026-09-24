// Package pathsafe validates relative paths taken from untrusted task data
// (mined commits, GitHub API responses, Gate/Ledger records) before they are
// used to read or write files, so a crafted path like "../../etc/passwd" or
// an absolute path can never escape the intended base directory.
package pathsafe

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrTraversal is returned when a path would resolve outside its base directory.
var ErrTraversal = errors.New("pathsafe: path escapes base directory")

// Join validates rel and joins it to base. It rejects empty paths, absolute
// paths, and any path whose cleaned form starts with ".." (including via
// internal ".." segments once cleaned) or otherwise resolves outside base.
// The returned path is absolute.
func Join(base, rel string) (string, error) {
	if rel == "" {
		return "", ErrTraversal
	}
	if filepath.IsAbs(rel) {
		return "", ErrTraversal
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrTraversal
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(filepath.Join(baseAbs, clean))
	if err != nil {
		return "", err
	}
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(filepath.Separator)) {
		return "", ErrTraversal
	}
	return fullAbs, nil
}

// Check reports whether rel is safe to join to some base directory, without
// needing the base on disk (useful for validating task data as it is mined,
// before any file is ever written).
func Check(rel string) bool {
	_, err := Join(string(filepath.Separator)+"base", rel)
	return err == nil
}
