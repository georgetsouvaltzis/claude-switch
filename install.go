package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/charmbracelet/huh"
)

var errClaudeMissing = errors.New("Claude Code isn't installed")

// findClaude checks PATH first, then the locations the official installers
// use, since a fresh install often isn't on PATH until the shell restarts.
func findClaude(home string) (path string, onPath bool, err error) {
	if p, err := exec.LookPath("claude"); err == nil {
		return p, true, nil
	}
	for _, c := range claudeCandidates(home) {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, false, nil
		}
	}
	if isWindows {
		if old, _ := filepath.Glob(filepath.Join(home, ".local", "bin", "claude.exe.old.*")); len(old) > 0 {
			return "", false, errors.New("claude.exe is missing but an interrupted update left " + filepath.Base(old[0]) + "; reinstall Claude Code")
		}
	}
	return "", false, errClaudeMissing
}

type installMethod struct {
	label   string
	command string
}

// Commands from https://code.claude.com/docs/en/setup
func installMethods() []installMethod {
	switch runtime.GOOS {
	case "windows":
		methods := []installMethod{{"Official installer (PowerShell)", "irm https://claude.ai/install.ps1 | iex"}}
		if _, err := exec.LookPath("winget"); err == nil {
			methods = append(methods, installMethod{"WinGet", "winget install Anthropic.ClaudeCode"})
		}
		return methods
	case "darwin":
		methods := []installMethod{{"Official installer", "curl -fsSL https://claude.ai/install.sh | bash"}}
		if _, err := exec.LookPath("brew"); err == nil {
			methods = append(methods, installMethod{"Homebrew", "brew install --cask claude-code"})
		}
		return methods
	default:
		return []installMethod{{"Official installer", "curl -fsSL https://claude.ai/install.sh | bash"}}
	}
}

func runInstall(m installMethod) error {
	var cmd *exec.Cmd
	if isWindows && m.label != "WinGet" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", m.command)
	} else if isWindows {
		cmd = exec.Command("cmd", "/C", m.command)
	} else {
		cmd = exec.Command("sh", "-c", m.command)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// ensureClaude returns the claude binary, offering to install it when
// missing. It never installs without an explicit choice.
func ensureClaude(home string, interactive bool) (string, error) {
	path, onPath, err := findClaude(home)
	if err == nil {
		if !onPath {
			warn(fmt.Sprintf("using %s, which isn't on your PATH; add %s to PATH", path, filepath.Dir(path)))
		}
		return path, nil
	}
	if !errors.Is(err, errClaudeMissing) {
		return "", err
	}

	methods := installMethods()
	if !interactive {
		return "", fmt.Errorf("%w; install it with: %s", errClaudeMissing, methods[0].command)
	}

	const skip = -1
	choice := 0
	opts := make([]huh.Option[int], 0, len(methods)+1)
	for i, m := range methods {
		opts = append(opts, huh.NewOption(m.label+"  "+dimSty.Render(m.command), i))
	}
	opts = append(opts, huh.NewOption("Don't install, I'll do it myself", skip))
	err = huh.NewSelect[int]().
		Title("Claude Code isn't installed. Install it now?").
		Description("csw runs the command below in your terminal.").
		Options(opts...).
		Value(&choice).
		WithTheme(theme()).
		Run()
	if err != nil {
		return "", err
	}
	if choice == skip {
		return "", fmt.Errorf("%w; install it with: %s", errClaudeMissing, methods[0].command)
	}
	if err := runInstall(methods[choice]); err != nil {
		return "", fmt.Errorf("install failed: %w", err)
	}
	path, onPath, err = findClaude(home)
	if err != nil {
		return "", fmt.Errorf("installed, but claude still can't be found; open a new terminal and retry")
	}
	if !onPath {
		warn(fmt.Sprintf("add %s to your PATH so plain `claude` works too", filepath.Dir(path)))
	}
	return path, nil
}
