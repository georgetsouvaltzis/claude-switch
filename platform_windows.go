//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
)

// Symlinks need Developer Mode or admin on Windows. Fall back to a junction
// for directories and a hard link for files. An editor's save-by-rename can
// split a hard link into a separate copy, which csw never replaces.
func makeLink(from, to string, isDir bool) error {
	if err := os.Symlink(from, to); err == nil {
		return nil
	}
	if isDir {
		return exec.Command("cmd", "/c", "mklink", "/J", to, from).Run()
	}
	return os.Link(from, to)
}

func removeLink(path string, isDir bool) error {
	return os.Remove(path)
}

// Windows has no exec(2): run claude as a child, let it own Ctrl-C, and
// mirror its exit code.
func execClaude(claude string, args, env []string) error {
	cmd := exec.Command(claude, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func claudeCandidates(home string) []string {
	candidates := []string{filepath.Join(home, ".local", "bin", "claude.exe")}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates, filepath.Join(local, "Microsoft", "WinGet", "Links", "claude.exe"))
	}
	return candidates
}

const isWindows = true
