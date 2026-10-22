package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	configDirEnv  = "CLAUDE_CONFIG_DIR"
	accountEnv    = "CSW_ACCOUNT"
	statusTimeout = 15 * time.Second
)

// Claude Code keys its macOS Keychain entry to CLAUDE_CONFIG_DIR whenever it
// is set, so pointing it at ~/.claude explicitly looks logged out. That one
// directory must run with the variable unset.
// envNameIs compares variable names the way the OS does: Windows ignores
// case, so Claude_Config_Dir would otherwise survive next to ours.
func envNameIs(name, want string) bool {
	if isWindows {
		return strings.EqualFold(name, want)
	}
	return name == want
}

func claudeEnv(environ []string, cfg *Config, a Account, home string) []string {
	env := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !envNameIs(name, configDirEnv) && !envNameIs(name, accountEnv) {
			env = append(env, kv)
		}
	}
	env = append(env, accountEnv+"="+a.Name)
	if dir := cfg.dir(a, home); !samePath(dir, defaultClaudeDir(home)) {
		env = append(env, configDirEnv+"="+dir)
	}
	return env
}

type linkResult struct {
	Linked    []string
	Conflicts []string
	Copied    []string
}

// linkShared links root items into an account directory. It never writes to
// src and never replaces an existing entry in dst.
func linkShared(src, dst string, items []string) (linkResult, error) {
	var res linkResult
	for _, item := range items {
		from := filepath.Join(src, item)
		to := filepath.Join(dst, item)
		fromInfo, err := os.Stat(from)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return res, err
		}
		if toInfo, err := os.Stat(to); err == nil {
			if !os.SameFile(fromInfo, toInfo) {
				res.Conflicts = append(res.Conflicts, item)
			}
			continue
		}
		if _, err := os.Lstat(to); err == nil {
			// dangling link left from an old root location
			res.Conflicts = append(res.Conflicts, item)
			continue
		}
		if err := makeLink(from, to, fromInfo.IsDir()); err != nil {
			return res, fmt.Errorf("link %s: %w", item, err)
		}
		res.Linked = append(res.Linked, item)
	}
	return res, nil
}

// unlinkShared removes only entries in dst that resolve to the same file as
// the root's item, i.e. our own links. Real files are left alone.
func unlinkShared(src, dst string, items []string) error {
	for _, item := range items {
		from := filepath.Join(src, item)
		to := filepath.Join(dst, item)
		fromInfo, err1 := os.Stat(from)
		toInfo, err2 := os.Stat(to)
		if err1 != nil || err2 != nil || !os.SameFile(fromInfo, toInfo) {
			continue
		}
		if err := removeLink(to, fromInfo.IsDir()); err != nil {
			return fmt.Errorf("unlink %s: %w", item, err)
		}
	}
	return nil
}

type authStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
	ConfigDirectory  string `json:"configDirectory"`
	Err              error  `json:"-"`
}

func fetchStatus(claude string, env []string) authStatus {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, "auth", "status", "--json")
	cmd.Env = env
	out, err := cmd.Output()
	var st authStatus
	if jsonErr := json.Unmarshal(out, &st); jsonErr != nil {
		switch {
		case ctx.Err() != nil:
			st.Err = errors.New("timed out")
		case err != nil:
			st.Err = err
		default:
			st.Err = jsonErr
		}
	}
	return st
}

func fetchStatuses(claude string, cfg *Config, home string) []authStatus {
	statuses := make([]authStatus, len(cfg.Accounts))
	var wg sync.WaitGroup
	for i, a := range cfg.Accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = fetchStatus(claude, claudeEnv(os.Environ(), cfg, a, home))
		}()
	}
	wg.Wait()
	return statuses
}

func runInteractive(claude string, env []string, args ...string) error {
	cmd := exec.Command(claude, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func logout(claude string, env []string) error {
	cmd := exec.Command(claude, "auth", "logout")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude auth logout: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
