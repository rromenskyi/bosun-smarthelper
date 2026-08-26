package filedump

import (
	"fmt"
	"path/filepath"
	"strings"
)

// safeJoin resolves a user-supplied relative path against root, refusing
// anything that would escape it — every handler in this package must
// route through this rather than joining paths itself. filepath.Clean
// alone isn't enough: it normalizes "a/../../etc" down to "../etc"
// without erroring, so the result still has to be checked against root
// after joining, not just pattern-matched for "..".
func safeJoin(root, userPath string) (string, error) {
	cleaned := filepath.Clean("/" + userPath) // leading slash makes Clean collapse any leading ".." components
	joined := filepath.Join(root, cleaned)

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if absJoined != absRoot && !strings.HasPrefix(absJoined, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the file store root", userPath)
	}
	return absJoined, nil
}

// relPath is safeJoin's inverse for display/index purposes: turns an
// absolute on-disk path back into the tree-relative, forward-slash form
// used in API responses and the sidecar index, regardless of host OS
// path separator.
func relPath(root, absPath string) (string, error) {
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// cleanRelPath normalizes a tree-relative path (e.g. from a JSON request
// body) to the same forward-slash, no-leading-slash form relPath
// produces, without touching the filesystem — used to compare/derive
// sidecar keys and path prefixes.
func cleanRelPath(p string) string {
	p = filepath.ToSlash(filepath.Clean("/" + p))
	return strings.TrimPrefix(p, "/")
}
