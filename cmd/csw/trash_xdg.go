//go:build !darwin && !windows

package main

import (
	"os"
	"path/filepath"
)

func moveToTrash(path, home string) (string, error) {
	return moveIntoXDGTrash(xdgTrashDir(home), path)
}

func xdgTrashDir(home string) string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "Trash")
}
