package main

import "path/filepath"

func moveToTrash(path, home string) (string, error) {
	return moveInto(filepath.Join(home, ".Trash"), path)
}
