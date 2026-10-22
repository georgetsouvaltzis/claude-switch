//go:build !windows

package main

import (
	"os"
	"syscall"
)

func makeLink(from, to string, _ bool) error {
	return os.Symlink(from, to)
}

func removeLink(path string, _ bool) error {
	return os.Remove(path)
}

func execClaude(claude string, args, env []string) error {
	return syscall.Exec(claude, append([]string{claude}, args...), env)
}

func claudeCandidates(home string) []string {
	return []string{
		home + "/.local/bin/claude",
		home + "/.claude/local/claude",
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		"/home/linuxbrew/.linuxbrew/bin/claude",
	}
}

const isWindows = false
