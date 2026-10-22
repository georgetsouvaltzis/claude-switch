package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	statuslineCommand = "csw statusline"
	wrapTimeout       = 5 * time.Second
)

type statuslineInput struct {
	TranscriptPath string `json:"transcript_path"`
}

// runStatusline is what Claude Code executes for every status line refresh.
// It must stay fast and never fail loudly: on any error it prints what it can.
func runStatusline(cfg *Config, p paths, stdin io.Reader, stdout io.Writer) {
	raw, _ := io.ReadAll(stdin)
	var in statuslineInput
	_ = json.Unmarshal(raw, &in)

	acct, ok := statuslineAccount(cfg, p.home, os.Getenv, in.TranscriptPath)
	segment := ""
	if ok {
		email := cmp.Or(loggedInEmail(cfg, acct, p.home), acct.Email)
		var colors palette
		if os.Getenv("NO_COLOR") == "" {
			colors = cfg.colorsFor(acct)
		}
		segment = renderSegment(acct.Name, email, colors)
	}

	wrapped := ""
	if cfg.StatuslineWrap != "" {
		wrapped = runWrapped(cfg.StatuslineWrap, raw)
	}
	fmt.Fprintln(stdout, joinStatusline(segment, wrapped))
}

// statuslineAccount identifies the session's account. The transcript path is
// the most reliable signal because it always lives under the session's
// config directory, whether or not env vars reach the status line process.
func statuslineAccount(cfg *Config, home string, getenv func(string) string, transcript string) (Account, bool) {
	if a, ok := cfg.find(getenv(accountEnv)); ok {
		return a, true
	}
	if transcript != "" {
		for _, a := range cfg.Accounts {
			if !cfg.isRoot(a, home) && within(transcript, cfg.dir(a, home)) {
				return a, true
			}
		}
	}
	if dir := getenv(configDirEnv); dir != "" {
		for _, a := range cfg.Accounts {
			if samePath(cfg.dir(a, home), dir) {
				return a, true
			}
		}
	}
	for _, a := range cfg.Accounts {
		if cfg.isRoot(a, home) {
			return a, true
		}
	}
	return Account{}, false
}

// loggedInEmail reads the login Claude is actually using, so the status line
// shows the truth even after a /login inside a session replaced it. Claude
// keeps the root account's file in the home folder, beside ~/.claude.
// oauthAccount is undocumented; callers fall back to the recorded email.
func loggedInEmail(cfg *Config, acct Account, home string) string {
	path := filepath.Join(cfg.dir(acct, home), ".claude.json")
	if samePath(cfg.dir(acct, home), filepath.Join(home, ".claude")) {
		path = filepath.Join(home, ".claude.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state struct {
		OAuthAccount struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	_ = json.Unmarshal(b, &state)
	return state.OAuthAccount.EmailAddress
}

// renderSegment draws plain text when colors is nil (NO_COLOR).
func renderSegment(name, email string, colors palette) string {
	seg := "● " + name
	if colors != nil {
		bold := "1"
		if c := colors["name"]; c != "" {
			bold += ";" + c
		}
		seg = paint(bold, seg)
	}
	if email != "" {
		seg += " " + paint(colors["email"], email)
	}
	return seg
}

func joinStatusline(segment, wrapped string) string {
	wrapped = strings.TrimRight(wrapped, "\n")
	switch {
	case segment == "":
		return wrapped
	case wrapped == "":
		return segment
	}
	first, rest, multi := strings.Cut(wrapped, "\n")
	line := segment + "  " + first
	if multi {
		line += "\n" + rest
	}
	return line
}

func runWrapped(command string, stdin []byte) string {
	ctx, cancel := context.WithTimeout(context.Background(), wrapTimeout)
	defer cancel()
	cmd := shellCommand(ctx, command)
	cmd.Stdin = bytes.NewReader(stdin)
	out, _ := cmd.Output()
	return string(out)
}

// shellCommand runs a wrapped status line the way Claude would have: on
// Windows that's Git Bash when installed, otherwise PowerShell.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if !isWindows {
		return exec.CommandContext(ctx, "sh", "-c", command)
	}
	if bash := gitBash(); bash != "" {
		return exec.CommandContext(ctx, bash, "-c", command)
	}
	return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
}

// gitBash finds Git for Windows' bash, never WSL's System32\bash.exe.
func gitBash() string {
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
		return p
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	// git.exe lives in <Git>\cmd or <Git>\bin; bash.exe in <Git>\bin.
	bash := filepath.Join(filepath.Dir(filepath.Dir(git)), "bin", "bash.exe")
	if _, err := os.Stat(bash); err != nil {
		return ""
	}
	return bash
}

type jsonMember struct {
	key   string
	value json.RawMessage
}

// readSettings keeps top-level key order so editing one key doesn't reshuffle
// the user's settings.json.
func readSettings(path string) ([]jsonMember, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(bytes.TrimSpace(b)) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%s: not a JSON object", path)
	}
	var members []jsonMember
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		members = append(members, jsonMember{key: tok.(string), value: v})
	}
	return members, nil
}

func encodeSettings(members []jsonMember) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, m := range members {
		key, _ := json.Marshal(m.key)
		var val bytes.Buffer
		if err := json.Indent(&val, m.value, "  ", "  "); err != nil {
			return nil, err
		}
		fmt.Fprintf(&buf, "  %s: %s", key, val.Bytes())
		if i < len(members)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

func statusLineCommandOf(members []jsonMember) (string, map[string]any) {
	for _, m := range members {
		if m.key == "statusLine" {
			var sl map[string]any
			if json.Unmarshal(m.value, &sl) == nil {
				cmd, _ := sl["command"].(string)
				return cmd, sl
			}
		}
	}
	return "", nil
}

func setMember(members []jsonMember, key string, value any) ([]jsonMember, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	for i := range members {
		if members[i].key == key {
			members[i].value = b
			return members, nil
		}
	}
	return append(members, jsonMember{key: key, value: b}), nil
}

func deleteMember(members []jsonMember, key string) []jsonMember {
	out := members[:0]
	for _, m := range members {
		if m.key != key {
			out = append(out, m)
		}
	}
	return out
}

// writeSettings backs the file up first and rewrites it in place rather than
// by rename, so account directories that hard-link it (Windows fallback)
// keep seeing the same file.
func writeSettings(path string, members []jsonMember) (backup string, err error) {
	out, err := encodeSettings(members)
	if err != nil {
		return "", err
	}
	if orig, err := os.ReadFile(path); err == nil {
		backup = path + ".csw-backup-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, orig, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return backup, os.WriteFile(path, out, 0o644)
}

type statuslineChange struct {
	Installed bool
	Wrapped   string
	Backup    string
}

func statuslineInstalled(cfg *Config, home string) bool {
	members, err := readSettings(filepath.Join(cfg.rootDir(home), "settings.json"))
	if err != nil {
		return false
	}
	cmd, _ := statusLineCommandOf(members)
	return cmd == statuslineCommand
}

func installStatusline(cfg *Config, home string) (statuslineChange, error) {
	path := filepath.Join(cfg.rootDir(home), "settings.json")
	members, err := readSettings(path)
	if err != nil {
		return statuslineChange{}, err
	}
	current, sl := statusLineCommandOf(members)
	if current == statuslineCommand {
		return statuslineChange{}, nil
	}
	if sl == nil {
		sl = map[string]any{}
	}
	sl["type"] = "command"
	sl["command"] = statuslineCommand
	if members, err = setMember(members, "statusLine", sl); err != nil {
		return statuslineChange{}, err
	}
	backup, err := writeSettings(path, members)
	if err != nil {
		return statuslineChange{}, err
	}
	cfg.StatuslineWrap = current
	return statuslineChange{Installed: true, Wrapped: current, Backup: backup}, nil
}

func uninstallStatusline(cfg *Config, home string) (statuslineChange, error) {
	path := filepath.Join(cfg.rootDir(home), "settings.json")
	members, err := readSettings(path)
	if err != nil {
		return statuslineChange{}, err
	}
	current, sl := statusLineCommandOf(members)
	if current != statuslineCommand {
		return statuslineChange{}, nil
	}
	if cfg.StatuslineWrap == "" {
		members = deleteMember(members, "statusLine")
	} else {
		sl["command"] = cfg.StatuslineWrap
		if members, err = setMember(members, "statusLine", sl); err != nil {
			return statuslineChange{}, err
		}
	}
	backup, err := writeSettings(path, members)
	if err != nil {
		return statuslineChange{}, err
	}
	restored := cfg.StatuslineWrap
	cfg.StatuslineWrap = ""
	return statuslineChange{Installed: true, Wrapped: restored, Backup: backup}, nil
}
