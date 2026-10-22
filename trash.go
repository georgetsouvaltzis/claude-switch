package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// moveInto renames path into dir under a unique name.
func moveInto(dir, path string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, trashName(path))
	return dest, renameNoCopy(path, dest)
}

func trashName(path string) string {
	return filepath.Base(path) + "-" + time.Now().Format("20060102-150405")
}

// renameNoCopy refuses rather than copying when dest is on another
// filesystem, so a purge never degrades into a permanent delete.
func renameNoCopy(path, dest string) error {
	err := os.Rename(path, dest)
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) && errors.Is(linkErr.Err, syscall.EXDEV) {
		return fmt.Errorf("%s is on a different disk than the trash (%s); move or delete it yourself", path, filepath.Dir(dest))
	}
	return err
}

// moveIntoXDGTrash follows the freedesktop.org Trash spec so file managers
// can show and restore the directory: the .trashinfo is created first, with
// O_EXCL, and claims the name in files/.
func moveIntoXDGTrash(trash, path string) (string, error) {
	infoDir, filesDir := filepath.Join(trash, "info"), filepath.Join(trash, "files")
	for _, d := range []string{infoDir, filesDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		(&url.URL{Path: abs}).EscapedPath(), time.Now().Format("2006-01-02T15:04:05"))

	base := trashName(path)
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s.%d", base, i)
		}
		infoPath := filepath.Join(infoDir, name+".trashinfo")
		f, err := os.OpenFile(infoPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(info)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		dest := filepath.Join(filesDir, name)
		if werr == nil {
			werr = renameNoCopy(path, dest)
		}
		if werr != nil {
			os.Remove(infoPath)
			return "", werr
		}
		return dest, nil
	}
}
