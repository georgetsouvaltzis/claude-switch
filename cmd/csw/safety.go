package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// markerFile marks a directory csw created; --purge refuses directories without it.
const markerFile = ".csw-account"

// samePath compares by file identity, so case-insensitive filesystems and
// symlinked paths can't make two names for one directory look different.
func samePath(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(ai, bi)
}

// within reports whether child is parent or lives anywhere beneath it.
func within(child, parent string) bool {
	pi, err := os.Stat(parent)
	if err != nil {
		return isLexicallyWithin(child, parent)
	}
	for p := child; ; p = filepath.Dir(p) {
		if ci, err := os.Stat(p); err == nil && os.SameFile(ci, pi) {
			return true
		}
		if filepath.Dir(p) == p {
			return false
		}
	}
}

func isLexicallyWithin(child, parent string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !startsWithParent(rel)
}

func startsWithParent(rel string) bool {
	return len(rel) >= 3 && rel[:2] == ".." && os.IsPathSeparator(rel[2])
}

func overlaps(a, b string) bool {
	return within(a, b) || within(b, a)
}

// checkAccountDir guards both creating and deleting account directories:
// never the root, never inside or above it, never home or a filesystem root,
// never overlapping another account.
func checkAccountDir(dir string, cfg *Config, home, self string) error {
	root := cfg.rootDir(home)
	switch {
	case filepath.Dir(dir) == dir:
		return fmt.Errorf("%s is a filesystem root", dir)
	case within(dir, root):
		return fmt.Errorf("%s is the root Claude directory or inside it (%s)", dir, shownPath(cfg.Root))
	case within(root, dir):
		return fmt.Errorf("%s contains the root Claude directory (%s)", dir, shownPath(cfg.Root))
	case within(home, dir):
		return fmt.Errorf("%s is or contains your home directory", dir)
	}
	for _, a := range cfg.Accounts {
		if a.Name != self && overlaps(dir, cfg.dir(a, home)) {
			return fmt.Errorf("%s overlaps account %q (%s)", dir, a.Name, shownPath(a.Dir))
		}
	}
	return nil
}

func checkPurgeable(a Account, cfg *Config, home string) (string, error) {
	if cfg.isRoot(a, home) {
		return "", errors.New("the root account can't be purged")
	}
	dir := cfg.dir(a, home)
	real, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s doesn't exist", shownPath(a.Dir))
	}
	if err != nil {
		return "", err
	}
	if err := checkAccountDir(real, cfg, home, a.Name); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(real, markerFile)); err != nil {
		return "", fmt.Errorf("%s wasn't created by csw (no %s marker), so csw won't delete it; to allow it: echo %s > %s", shownPath(a.Dir), markerFile, a.Name, shownPath(a.Dir+"/"+markerFile))
	}
	return real, nil
}

// isLink reports symlinks and, on Windows, junctions: since Go 1.23 those
// have ModeIrregular instead of ModeSymlink, and are not ModeDir either.
func isLink(m fs.FileMode) bool {
	return m&(fs.ModeSymlink|fs.ModeIrregular) != 0
}

// deleteAccountDir permanently deletes a folder that passed checkPurgeable.
// Shared links go first, so nothing can reach into the root through them;
// os.RemoveAll removes any other link without following it.
func deleteAccountDir(root, path string, shared []string) error {
	if err := unlinkShared(root, path, shared); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func writeMarker(dir, name string) error {
	return os.WriteFile(filepath.Join(dir, markerFile), []byte(name+"\n"), 0o644)
}
