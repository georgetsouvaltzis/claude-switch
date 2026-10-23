package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type fixture struct {
	home string
	root string
	cfg  *Config
}

// newFixture builds a fake home with a populated root folder and a csw-made
// "work" account that links the shared items.
func newFixture(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".claude")
	mustWrite(t, filepath.Join(root, "settings.json"), `{"model":"opus"}`)
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "rules")
	mustWrite(t, filepath.Join(root, "skills", "s", "SKILL.md"), "skill")
	mustWrite(t, filepath.Join(root, "projects", "p", "root-session.jsonl"), "root")
	cfg := newConfig("~/.claude", home)
	cfg.Accounts = []Account{{Name: "personal", Dir: "~/.claude"}, {Name: "work", Dir: "~/.claude-work"}}

	work := filepath.Join(home, ".claude-work")
	must(t, os.MkdirAll(work, 0o700))
	must(t, writeMarker(work, "work"))
	_, err := linkShared(root, work, cfg.Shared)
	must(t, err)
	mustWrite(t, filepath.Join(work, "projects", "p", "work-session.jsonl"), "work")
	return fixture{home: home, root: root, cfg: cfg}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args []string
		want command
	}{
		{nil, command{kind: "launch"}},
		{[]string{"-p", "hi"}, command{kind: "launch", args: []string{"-p", "hi"}}},
		{[]string{"-a", "work", "--resume"}, command{kind: "launch", account: "work", args: []string{"--resume"}}},
		{[]string{"--account=work", "--", "status"}, command{kind: "launch", account: "work", args: []string{"status"}, literal: true}},
		{[]string{"--", "status"}, command{kind: "launch", args: []string{"status"}, literal: true}},
		{[]string{"remove", "work"}, command{kind: "remove", args: []string{"work"}}},
		{[]string{"--version"}, command{kind: "version"}},
		{[]string{"--help"}, command{kind: "help"}},
		{[]string{"doctor"}, command{kind: "launch", args: []string{"doctor"}}},
	}
	for _, tt := range tests {
		got, err := parseArgs(tt.args)
		must(t, err)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
		}
	}
	if _, err := parseArgs([]string{"-a"}); err == nil {
		t.Error("-a without a name should fail")
	}
}

func TestParseFlagsInterspersed(t *testing.T) {
	specs := map[string]flagSpec{
		"purge":    {names: []string{"--purge"}},
		"sessions": {names: []string{"--sessions"}, hasValue: true},
		"dry-run":  {names: []string{"-n", "--dry-run"}},
	}
	flags, pos, err := parseFlags([]string{"--purge", "work", "-n", "--sessions=move"}, specs)
	must(t, err)
	if !reflect.DeepEqual(pos, []string{"work"}) || flags["purge"] != "true" || flags["dry-run"] != "true" || flags["sessions"] != "move" {
		t.Errorf("flags=%v pos=%v", flags, pos)
	}
	for _, bad := range [][]string{{"--nope"}, {"--sessions"}, {"--purge=yes"}} {
		if _, _, err := parseFlags(bad, specs); err == nil {
			t.Errorf("parseFlags(%q) should fail", bad)
		}
	}
	if _, _, err := parseFlags([]string{"work", "-h"}, specs); err != errHelp {
		t.Errorf("-h should request help, got %v", err)
	}
}

func TestClaudeEnv(t *testing.T) {
	f := newFixture(t)
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/stale", "CSW_ACCOUNT=stale"}

	got := claudeEnv(base, f.cfg, f.cfg.Accounts[0], f.home)
	if want := []string{"PATH=/bin", "CSW_ACCOUNT=personal"}; !reflect.DeepEqual(got, want) {
		t.Errorf("~/.claude must run with CLAUDE_CONFIG_DIR unset: got %q", got)
	}

	got = claudeEnv(base, f.cfg, f.cfg.Accounts[1], f.home)
	want := []string{"PATH=/bin", "CSW_ACCOUNT=work", "CLAUDE_CONFIG_DIR=" + filepath.Join(f.home, ".claude-work")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("work env = %q, want %q", got, want)
	}

	custom := Account{Name: "main", Dir: filepath.Join(f.home, "custom-root")}
	got = claudeEnv(nil, f.cfg, custom, f.home)
	if !slices.Contains(got, "CLAUDE_CONFIG_DIR="+custom.Dir) {
		t.Errorf("a non-default root still needs the var: %q", got)
	}
}

func TestLinkSharedNeverReplacesOrWritesRoot(t *testing.T) {
	f := newFixture(t)
	before := snapshot(t, f.root)

	dst := filepath.Join(f.home, "acct")
	mustWrite(t, filepath.Join(dst, "CLAUDE.md"), "mine")
	res, err := linkShared(f.root, dst, f.cfg.Shared)
	must(t, err)
	if want := []string{"settings.json", "skills"}; !reflect.DeepEqual(res.Linked, want) {
		t.Errorf("linked = %q, want %q", res.Linked, want)
	}
	if want := []string{"CLAUDE.md"}; !reflect.DeepEqual(res.Conflicts, want) {
		t.Errorf("conflicts = %q, want %q", res.Conflicts, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "CLAUDE.md")); string(b) != "mine" {
		t.Error("existing file was replaced")
	}

	res, err = linkShared(f.root, dst, f.cfg.Shared)
	must(t, err)
	if len(res.Linked) != 0 {
		t.Errorf("second run relinked %q", res.Linked)
	}

	must(t, unlinkShared(f.root, dst, f.cfg.Shared))
	if _, err := os.Lstat(filepath.Join(dst, "skills")); err == nil {
		t.Error("link survived unlinkShared")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "CLAUDE.md")); string(b) != "mine" {
		t.Error("unlinkShared removed a real file")
	}
	assertSnapshot(t, f.root, before)
}

func TestCheckAccountDirRefusesDangerousPaths(t *testing.T) {
	f := newFixture(t)
	alias := filepath.Join(f.home, ".claude-alias")
	must(t, os.Symlink(f.root, alias))

	refused := map[string]string{
		"root":                   f.root,
		"inside root":            filepath.Join(f.root, "work"),
		"symlink to root":        alias,
		"root with odd spelling": f.root + string(filepath.Separator) + ".",
		"home":                   f.home,
		"parent of home":         filepath.Dir(f.home),
		"filesystem root":        string(filepath.Separator),
		"other account":          filepath.Join(f.home, ".claude-work"),
		"inside other account":   filepath.Join(f.home, ".claude-work", "nested"),
	}
	for name, dir := range refused {
		if err := checkAccountDir(dir, f.cfg, f.home, "new"); err == nil {
			t.Errorf("%s (%s) was allowed", name, dir)
		}
	}
	// Each root guard must hold on its own, not only via the overlap check
	// against the root account.
	bare := &Config{Root: f.cfg.Root}
	for _, name := range []string{"root", "inside root", "symlink to root", "root with odd spelling", "home", "parent of home", "filesystem root"} {
		if err := checkAccountDir(refused[name], bare, f.home, "new"); err == nil {
			t.Errorf("without accounts, %s (%s) was allowed", name, refused[name])
		}
	}
	if err := checkAccountDir(filepath.Join(f.home, ".claude-new"), f.cfg, f.home, "new"); err != nil {
		t.Errorf("sibling dir refused: %v", err)
	}
	if err := checkAccountDir(filepath.Join(f.home, ".claude-work"), f.cfg, f.home, "work"); err != nil {
		t.Errorf("an account's own dir refused: %v", err)
	}
}

func TestCheckAccountDirWithRootOutsideHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "data", "claude")
	must(t, os.MkdirAll(home, 0o700))
	must(t, os.MkdirAll(root, 0o700))
	cfg := &Config{Root: root}

	for name, dir := range map[string]string{
		"home":           home,
		"parent of root": filepath.Join(base, "data"),
		"inside root":    filepath.Join(root, "x"),
	} {
		if err := checkAccountDir(dir, cfg, home, "new"); err == nil {
			t.Errorf("%s (%s) was allowed", name, dir)
		}
	}
	if err := checkAccountDir(filepath.Join(home, ".claude-new"), cfg, home, "new"); err != nil {
		t.Errorf("normal dir refused: %v", err)
	}
}

func TestCheckPurgeable(t *testing.T) {
	f := newFixture(t)
	if _, err := checkPurgeable(f.cfg.Accounts[0], f.cfg, f.home); err == nil {
		t.Error("root account was purgeable")
	}

	manual := filepath.Join(f.home, ".claude-manual")
	must(t, os.MkdirAll(manual, 0o700))
	if _, err := checkPurgeable(Account{Name: "manual", Dir: manual}, f.cfg, f.home); err == nil {
		t.Error("dir without marker was purgeable")
	}

	alias := filepath.Join(f.home, ".claude-alias")
	must(t, os.Symlink(f.root, alias))
	mustWrite(t, filepath.Join(f.root, markerFile), "planted")
	defer os.Remove(filepath.Join(f.root, markerFile))
	if _, err := checkPurgeable(Account{Name: "alias", Dir: alias}, f.cfg, f.home); err == nil {
		t.Error("symlink to root was purgeable even with a planted marker")
	}

	real, err := checkPurgeable(f.cfg.Accounts[1], f.cfg, f.home)
	must(t, err)
	if !samePath(real, filepath.Join(f.home, ".claude-work")) {
		t.Errorf("purge path = %s", real)
	}
}

func TestPurgeLeavesRootByteIdentical(t *testing.T) {
	f := newFixture(t)
	// A link csw didn't make, pointing into the root: deleting must not follow it.
	must(t, os.Symlink(filepath.Join(f.root, "projects"), filepath.Join(f.home, ".claude-work", "stray-link")))
	before := snapshot(t, f.root)

	real, err := checkPurgeable(f.cfg.Accounts[1], f.cfg, f.home)
	must(t, err)
	must(t, deleteAccountDir(f.root, real, f.cfg.Shared))

	if _, err := os.Lstat(real); err == nil {
		t.Error("account dir still exists")
	}
	assertSnapshot(t, f.root, before)
}

func TestMoveSessionsNeverOverwrites(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(f.home, ".claude-work", "projects")
	dst := filepath.Join(f.root, "projects")
	mustWrite(t, filepath.Join(src, "p", "root-session.jsonl"), "work copy")

	moved, conflicts, err := moveSessions(src, dst)
	must(t, err)
	if moved != 1 || conflicts != 1 {
		t.Errorf("moved=%d conflicts=%d, want 1 and 1", moved, conflicts)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "p", "root-session.jsonl")); string(b) != "root" {
		t.Error("root session was overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "p", "work-session.jsonl")); string(b) != "work" {
		t.Error("work session not moved")
	}

	shared := filepath.Join(f.home, "shared-acct")
	must(t, os.MkdirAll(shared, 0o700))
	must(t, os.Symlink(dst, filepath.Join(shared, "projects")))
	moved, _, err = moveSessions(filepath.Join(shared, "projects"), dst)
	must(t, err)
	if moved != 0 {
		t.Error("followed a projects symlink into root")
	}
}

func TestStatuslineAccount(t *testing.T) {
	f := newFixture(t)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	transcript := filepath.Join(f.home, ".claude-work", "projects", "p", "x.jsonl")
	rootTranscript := filepath.Join(f.root, "projects", "p", "y.jsonl")

	cases := []struct {
		name, transcript string
		env              map[string]string
		want             string
	}{
		{"CSW_ACCOUNT wins", rootTranscript, map[string]string{accountEnv: "work"}, "work"},
		{"transcript path", transcript, nil, "work"},
		{"config dir env", "", map[string]string{configDirEnv: filepath.Join(f.home, ".claude-work")}, "work"},
		{"root transcript", rootTranscript, nil, "personal"},
		{"fallback to root", "", nil, "personal"},
	}
	for _, c := range cases {
		a, ok := statuslineAccount(f.cfg, f.home, env(c.env), c.transcript)
		if !ok || a.Name != c.want {
			t.Errorf("%s: got %q, want %q", c.name, a.Name, c.want)
		}
	}
}

func TestStatuslineRendering(t *testing.T) {
	if got := renderSegment("work", "me@company.com", nil); got != "● work me@company.com" {
		t.Errorf("segment = %q", got)
	}
	if got := renderSegment("work", "", nil); got != "● work" {
		t.Errorf("segment without email = %q", got)
	}
	if got := joinStatusline("● work", "theirs\nline2\n"); got != "● work  theirs\nline2" {
		t.Errorf("join = %q", got)
	}
	if got := joinStatusline("", "theirs"); got != "theirs" {
		t.Errorf("join without segment = %q", got)
	}
}

func TestStatuslineIgnoresRateLimits(t *testing.T) {
	f := newFixture(t)
	t.Setenv(accountEnv, "work")
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	runStatusline(f.cfg, paths{home: f.home}, strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":31}}}`), &out)
	if got := strings.TrimSpace(out.String()); got != "● work" {
		t.Errorf("got %q; usage must not be shown", got)
	}
}

func TestStatuslineInstallPreservesSettings(t *testing.T) {
	f := newFixture(t)
	settings := filepath.Join(f.root, "settings.json")
	orig := `{
  "model": "opus",
  "statusLine": {"type": "command", "command": "~/.claude/mine.sh", "padding": 0},
  "permissions": {"allow": ["Bash(ls)"]}
}`
	mustWrite(t, settings, orig)

	ch, err := installStatusline(f.cfg, f.home)
	must(t, err)
	if !ch.Installed || ch.Wrapped != "~/.claude/mine.sh" || f.cfg.StatuslineWrap != "~/.claude/mine.sh" {
		t.Fatalf("install = %+v, wrap = %q", ch, f.cfg.StatuslineWrap)
	}
	if b, _ := os.ReadFile(ch.Backup); string(b) != orig {
		t.Error("backup doesn't match the original")
	}
	members, err := readSettings(settings)
	must(t, err)
	var keys []string
	for _, m := range members {
		keys = append(keys, m.key)
	}
	if want := []string{"model", "statusLine", "permissions"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("key order = %q, want %q", keys, want)
	}
	if cmd, sl := statusLineCommandOf(members); cmd != statuslineCommand || sl["padding"] != float64(0) {
		t.Errorf("statusLine = %v", sl)
	}
	if ch, _ := installStatusline(f.cfg, f.home); ch.Installed {
		t.Error("second install rewrote settings")
	}

	_, err = uninstallStatusline(f.cfg, f.home)
	must(t, err)
	members, _ = readSettings(settings)
	if cmd, _ := statusLineCommandOf(members); cmd != "~/.claude/mine.sh" {
		t.Errorf("uninstall restored %q", cmd)
	}
	if !strings.Contains(string(mustRead(t, settings)), `"Bash(ls)"`) {
		t.Error("unrelated settings lost")
	}
}

func TestConfigRoundTripAndLookup(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "csw", "config.toml")
	if _, err := loadConfig(path); err != errNoConfig {
		t.Fatalf("missing config: %v", err)
	}
	cfg := newConfig("~/.claude", home)
	cfg.Accounts = []Account{{Name: "personal", Dir: "~/.claude", Email: "a@b.c"}, {Name: "work", Dir: "~/.claude-work"}}
	must(t, saveConfig(path, cfg))
	got, err := loadConfig(path)
	must(t, err)
	want := *cfg
	want.StatuslineColors = cfg.StatuslineColors.withDefaults()
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("reloaded = %+v", got)
	}
	if _, err := got.lookup("wrok"); err == nil || !strings.Contains(err.Error(), `did you mean "work"`) {
		t.Errorf("lookup typo: %v", err)
	}
}

func TestLoadConfigMigratesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	mustWrite(t, path, `shared = ["settings.json"]

[[accounts]]
  name = "default"
  dir = "~/.claude"

[[accounts]]
  name = "work"
  dir = "~/.claude-work"
`)
	cfg, err := loadConfig(path)
	must(t, err)
	if cfg.Root != "~/.claude" || len(cfg.Accounts) != 2 {
		t.Fatalf("migrated = %+v", cfg)
	}
	if !strings.Contains(string(mustRead(t, path)), `root = "~/.claude"`) {
		t.Error("migration not saved")
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	must(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		sum := sha256.Sum256(mustRead(t, path))
		rel, _ := filepath.Rel(dir, path)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	}))
	return out
}

func assertSnapshot(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	if after := snapshot(t, dir); !reflect.DeepEqual(after, before) {
		t.Errorf("root changed:\nbefore %v\nafter  %v", before, after)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(content), 0o644))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestChooseRemovableWithoutTTY(t *testing.T) {
	f := newFixture(t)
	a := &app{p: paths{home: f.home}, cfg: f.cfg}
	_, err := a.chooseRemovable()
	if err == nil || !strings.Contains(err.Error(), "remove needs an account name: work") || strings.Contains(err.Error(), "personal") {
		t.Fatalf("want removable names without the root account, got %v", err)
	}

	a.cfg.Accounts = a.cfg.Accounts[:1]
	if _, err := a.chooseRemovable(); err == nil || !strings.Contains(err.Error(), "personal is the root account") {
		t.Fatalf("want root-only error, got %v", err)
	}
}

func TestCheckNameRejectsCommandNames(t *testing.T) {
	for _, bad := range []string{"status", "add", "remove", "mcp", "update", "-x", "a b"} {
		if checkName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"work", "personal", "client-2"} {
		if err := checkName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}

func TestAccountFromArgs(t *testing.T) {
	cfg := &Config{Accounts: []Account{{Name: "personal"}, {Name: "work"}}}
	cases := []struct {
		in   []string
		want command
	}{
		{[]string{"work"}, command{kind: "launch", account: "work", args: []string{}}},
		{[]string{"work", "--resume"}, command{kind: "launch", account: "work", args: []string{"--resume"}}},
		{[]string{"work", "--", "work"}, command{kind: "launch", account: "work", args: []string{"work"}, literal: true}},
		{[]string{"--", "work"}, command{kind: "launch", args: []string{"work"}, literal: true}},
		{[]string{"-a", "personal", "work"}, command{kind: "launch", account: "personal", args: []string{"work"}}},
		{[]string{"fix the bug"}, command{kind: "launch", args: []string{"fix the bug"}}},
		{[]string{"work", "--save"}, command{kind: "launch", account: "work", args: []string{}, save: true}},
		{[]string{"work", "--save", "--resume"}, command{kind: "launch", account: "work", args: []string{"--resume"}, save: true}},
		{[]string{"--save", "work"}, command{kind: "launch", account: "work", args: []string{}, save: true}},
		{[]string{"work", "--resume", "--save"}, command{kind: "launch", account: "work", args: []string{"--resume", "--save"}}},
		{[]string{"work", "--", "--save"}, command{kind: "launch", account: "work", args: []string{"--save"}, literal: true}},
	}
	for _, c := range cases {
		cmd, err := parseArgs(c.in)
		must(t, err)
		if got := accountFromArgs(cmd, cfg); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestStrayWord(t *testing.T) {
	cfg := &Config{Accounts: []Account{{Name: "personal"}, {Name: "work"}}}
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"fix", "the", "account"}, "fix"},
		{[]string{"hello"}, "hello"},
		{[]string{"wrk", "--resume"}, "wrk"},
		{[]string{"work", "fix", "it"}, "fix"},
		{[]string{"fix the account"}, ""},
		{[]string{"work", "fix the account"}, ""},
		{[]string{"--", "fix", "it"}, ""},
		{[]string{"work", "--", "fix"}, ""},
		{[]string{"--resume"}, ""},
		{[]string{"mcp", "list"}, ""},
		{[]string{"work"}, ""},
	}
	for _, c := range cases {
		cmd, err := parseArgs(c.in)
		must(t, err)
		if got := strayWord(accountFromArgs(cmd, cfg)); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStrayWordError(t *testing.T) {
	accounts := []string{"personal", "work"}
	err := strayWordError(command{args: []string{"fix", "the", "account"}}, "fix", "", accounts)
	for _, want := range []string{`"fix" isn't an account or command (accounts: personal, work)`, `quote it: csw "fix the account"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	err = strayWordError(command{account: "work", args: []string{"hello"}}, "hello", "", accounts)
	if !strings.Contains(err.Error(), "csw work -- hello") {
		t.Errorf("want one-word hint, got:\n%v", err)
	}
	err = strayWordError(command{args: []string{"stauts"}}, "stauts", "status", accounts)
	if !strings.Contains(err.Error(), "did you mean: csw status") {
		t.Errorf("want suggestion, got:\n%v", err)
	}
}

func TestClaudeCommandLine(t *testing.T) {
	if got := claudeCommandLine([]string{"--resume", "fix the account"}); got != `then: claude --resume "fix the account"` {
		t.Errorf("got %s", got)
	}
	if claudeCommandLine(nil) != "" {
		t.Error("want empty for no args")
	}
}

func TestParseArgsSaveClear(t *testing.T) {
	cases := []struct {
		in   []string
		want command
	}{
		{[]string{"--save"}, command{kind: "launch", save: true, args: []string{}}},
		{[]string{"--save", "--", "hi"}, command{kind: "launch", save: true, args: []string{"hi"}, literal: true}},
		{[]string{"-a", "work", "--save"}, command{kind: "launch", account: "work", save: true, args: []string{}}},
		{[]string{"--clear"}, command{kind: "clear-default"}},
	}
	for _, c := range cases {
		got, err := parseArgs(c.in)
		must(t, err)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.in, got, c.want)
		}
	}
	if _, err := parseArgs([]string{"--clear", "work"}); err == nil {
		t.Error("--clear with anything else must be refused")
	}
}

func TestChooseAccountUsesDefault(t *testing.T) {
	t.Setenv(accountEnv, "")
	last := filepath.Join(t.TempDir(), "last")
	a := &app{p: paths{last: last}, cfg: &Config{Accounts: []Account{{Name: "personal"}, {Name: "work"}}}}

	must(t, saveLastUsed(last, "work"))
	if _, _, err := a.chooseAccount("", nil); err == nil {
		t.Error("last used alone must not skip the picker; want the no-TTY error")
	}

	a.cfg.Default = "work"
	acct, fromDefault, err := a.chooseAccount("", nil)
	if err != nil || acct.Name != "work" || !fromDefault {
		t.Errorf("want default work, got %q fromDefault=%v err=%v", acct.Name, fromDefault, err)
	}
	if acct, fromDefault, _ := a.chooseAccount("personal", nil); acct.Name != "personal" || fromDefault {
		t.Errorf("explicit name must win over the default, got %q", acct.Name)
	}
	t.Setenv(accountEnv, "personal")
	if acct, _, _ := a.chooseAccount("", nil); acct.Name != "personal" {
		t.Errorf("CSW_ACCOUNT must win over the default, got %q", acct.Name)
	}

	t.Setenv(accountEnv, "")
	a.cfg.remove("work")
	if a.cfg.Default != "" {
		t.Errorf("removing the default account must clear it, got %q", a.cfg.Default)
	}
}

func TestLoggedInEmail(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.home, ".claude.json"), `{"oauthAccount":{"emailAddress":"me@example.com"}}`)
	mustWrite(t, filepath.Join(f.home, ".claude-work", ".claude.json"), `{"oauthAccount":{"emailAddress":"me@company.com"}}`)
	personal, _ := f.cfg.find("personal")
	work, _ := f.cfg.find("work")
	if got := loggedInEmail(f.cfg, personal, f.home); got != "me@example.com" {
		t.Errorf("root account must read ~/.claude.json, got %q", got)
	}
	if got := loggedInEmail(f.cfg, work, f.home); got != "me@company.com" {
		t.Errorf("work must read its own .claude.json, got %q", got)
	}
	if got := loggedInEmail(f.cfg, Account{Name: "gone", Dir: "~/.claude-gone"}, f.home); got != "" {
		t.Errorf("missing file must give empty, got %q", got)
	}
}

func TestStatuslineColors(t *testing.T) {
	cases := map[string]string{"#ff8800": "38;2;255;136;0", "#F80": "38;2;255;136;0", "245": "38;5;245", " 0 ": "38;5;0"}
	for in, want := range cases {
		if got, err := sgr(in); err != nil || got != want {
			t.Errorf("sgr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"256", "-1", "#ggg", "red", "#12345"} {
		if _, err := sgr(bad); err == nil {
			t.Errorf("sgr(%q) accepted", bad)
		}
	}

	c := StatuslineColors{Email: "#112233", Name: "nope"}
	p := c.resolve()
	if p["email"] != "38;2;17;34;51" || p["name"] != "38;5;173" {
		t.Errorf("resolve = %v", p)
	}
	if bad := c.invalid(); len(bad) != 1 || !strings.HasPrefix(bad[0], "name") {
		t.Errorf("invalid = %v", bad)
	}
	seg := renderSegment("work", "me@company.com", p)
	for _, want := range []string{"\x1b[1;38;5;173m● work", "\x1b[38;2;17;34;51mme@company.com"} {
		if !strings.Contains(seg, want) {
			t.Errorf("missing %q in %q", want, seg)
		}
	}
}

func TestStatuslineColorsAlwaysWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	mustWrite(t, path, "root = \"~/.claude\"\n\n[[accounts]]\n  name = \"personal\"\n  dir = \"~/.claude\"\n")
	cfg, err := loadConfig(path)
	must(t, err)
	if !cfg.colorsOutdated {
		t.Fatal("a config without [statusline_colors] must be flagged for rewrite")
	}
	cfg.StatuslineColors.Email = "#abcdef"
	must(t, saveConfig(path, cfg))

	b, _ := os.ReadFile(path)
	for _, want := range []string{"[statusline_colors]", `email = "#abcdef"`, `name = "173"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %q in:\n%s", want, b)
		}
	}
	got, err := loadConfig(path)
	must(t, err)
	if got.colorsOutdated || got.StatuslineColors.Email != "#abcdef" || len(got.Accounts) != 1 {
		t.Errorf("round trip: %+v", got)
	}

	mustWrite(t, path, "root = \"~/.claude\"\n\n[statusline_colors]\n  email = \"#4DB6AC\"\n")
	partial, err := loadConfig(path)
	must(t, err)
	if !partial.colorsOutdated || partial.StatuslineColors.Email != "#4DB6AC" {
		t.Errorf("a partial section must be flagged and keep its values: %+v", partial)
	}

	mustWrite(t, path, "root = \"~/.claude\"\n\n[statusline_colors]\n  name = \"173\"\n  email = \"245\"\n  high = \"1\"\n")
	stale, err := loadConfig(path)
	must(t, err)
	if !stale.colorsOutdated {
		t.Error("a key csw no longer uses must be flagged so it gets dropped")
	}
	if cfg.StatuslineColors.Name != "" {
		t.Error("saving must not change the in-memory config")
	}
}

func TestPerAccountColors(t *testing.T) {
	cfg := &Config{
		StatuslineColors: StatuslineColors{Name: "#112233"},
		Accounts: []Account{
			{Name: "personal"},
			{Name: "work", Colors: StatuslineColors{Name: "#445566", Email: "nope"}},
		},
	}
	if got := cfg.colorsFor(cfg.Accounts[0])["name"]; got != "38;2;17;34;51" {
		t.Errorf("account without own colors must use the global ones, got %q", got)
	}
	work := cfg.colorsFor(cfg.Accounts[1])
	if work["name"] != "38;2;68;85;102" {
		t.Errorf("own color must win, got %q", work["name"])
	}
	if work["email"] != "38;5;245" {
		t.Errorf("invalid own color must fall back to global, then default; got %q", work["email"])
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	cfg.Root = "~/.claude"
	must(t, saveConfig(path, cfg))
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "[accounts.colors]") != 1 {
		t.Errorf("only the account with own colors gets a colors table:\n%s", b)
	}
	got, err := loadConfig(path)
	must(t, err)
	if got.Accounts[1].Colors.Name != "#445566" || got.Accounts[0].Colors != (StatuslineColors{}) {
		t.Errorf("round trip: %+v", got.Accounts)
	}
	if got.colorsOutdated {
		t.Error("per-account colors must not trigger a rewrite")
	}
}

func TestSyncSharedLinksNewRootItems(t *testing.T) {
	f := newFixture(t)
	a := &app{p: paths{home: f.home}, cfg: f.cfg}
	work, _ := f.cfg.find("work")
	mustWrite(t, filepath.Join(f.root, "agents", "reviewer.md"), "agent")
	before := snapshot(t, f.root)

	a.syncShared(work)
	if b, err := os.ReadFile(filepath.Join(f.home, ".claude-work", "agents", "reviewer.md")); err != nil || string(b) != "agent" {
		t.Errorf("new root item not shared on launch: %v", err)
	}

	personal, _ := f.cfg.find("personal")
	a.syncShared(personal)
	assertSnapshot(t, f.root, before)
}

func TestLoginOverrides(t *testing.T) {
	f := newFixture(t)
	a := &app{p: paths{home: f.home}, cfg: f.cfg}
	for _, v := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		t.Setenv(v, "")
	}
	if got := a.loginOverrides(); len(got) != 0 {
		t.Fatalf("clean setup flagged: %q", got)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	mustWrite(t, filepath.Join(f.root, "settings.json"), `{"apiKeyHelper": "echo key"}`)
	got := a.loginOverrides()
	if len(got) != 2 || !strings.Contains(got[0], "ANTHROPIC_API_KEY") || !strings.Contains(got[1], "apiKeyHelper") {
		t.Errorf("overrides = %q", got)
	}
}

func TestStrayCheck(t *testing.T) {
	names := []string{"personal", "work"}
	cases := []struct {
		in               []string
		word, suggestion string
	}{
		{[]string{"hey"}, "", ""},                  // csw "hey": a prompt
		{[]string{"work", "hey"}, "", ""},          // csw work "hey"
		{[]string{"wrk"}, "wrk", "work"},           // one keystroke off an account
		{[]string{"stauts"}, "stauts", "status"},   // swapped pair counts as one
		{[]string{"hep"}, "hep", "help"},           // one deletion
		{[]string{"fix", "the", "bug"}, "fix", ""}, // several bare words: refused
		{[]string{"fix the bug"}, "", ""},          // quoted: a prompt
		{[]string{"work", "fix", "it"}, "fix", ""}, // several bare words after an account
		{[]string{"--", "wrk"}, "", ""},            // forced prompt
	}
	for _, c := range cases {
		cmd, err := parseArgs(c.in)
		must(t, err)
		cmd = accountFromArgs(cmd, &Config{Accounts: []Account{{Name: "personal"}, {Name: "work"}}})
		word, suggestion := strayCheck(cmd, names)
		if word != c.word || suggestion != c.suggestion {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", c.in, word, suggestion, c.word, c.suggestion)
		}
	}
}

func TestRename(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	p := paths{home: f.home, config: filepath.Join(dir, "config.toml"), last: filepath.Join(dir, "last")}
	f.cfg.Default = "work"
	a := &app{p: p, cfg: f.cfg}
	must(t, saveLastUsed(p.last, "work"))

	must(t, a.rename([]string{"work", "client"}, false))
	acct, ok := a.cfg.find("client")
	if !ok || acct.Dir != "~/.claude-work" {
		t.Fatalf("renamed account = %+v, %v; folder must not move", acct, ok)
	}
	if _, ok := a.cfg.find("work"); ok {
		t.Error("old name still present")
	}
	if a.cfg.Default != "client" || loadLastUsed(p.last) != "client" {
		t.Errorf("default %q / last %q not carried over", a.cfg.Default, loadLastUsed(p.last))
	}
	if b, _ := os.ReadFile(filepath.Join(f.home, ".claude-work", markerFile)); strings.TrimSpace(string(b)) != "client" {
		t.Errorf("marker = %q", b)
	}
	saved, err := loadConfig(p.config)
	must(t, err)
	if _, ok := saved.find("client"); !ok {
		t.Error("rename not saved")
	}

	for _, bad := range [][]string{{"client", "personal"}, {"client", "status"}, {"nope", "x"}, {"client", "a b"}} {
		if err := a.rename(bad, false); err == nil {
			t.Errorf("rename %q accepted", bad)
		}
	}
}

func TestMoveTarget(t *testing.T) {
	if got := moveTarget("/home/u/.claude-work", "client"); got != "/home/u/.claude-client" {
		t.Errorf("got %q", got)
	}
	if got := moveTarget("/home/u/accounts/acme", "client"); got != "/home/u/accounts/.claude-client" {
		t.Errorf("got %q", got)
	}
}

func TestRenameMoveRefusals(t *testing.T) {
	f := newFixture(t)
	a := &app{p: paths{home: f.home}, cfg: f.cfg, claude: "/bin/false"}
	personal, _ := f.cfg.find("personal")
	if err := a.renameAndMove(personal, "main"); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("root must not move: %v", err)
	}
	must(t, os.MkdirAll(filepath.Join(f.home, ".claude-client"), 0o700))
	work, _ := f.cfg.find("work")
	if err := a.renameAndMove(work, "client"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("existing target must be refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".claude-work")); err != nil {
		t.Error("refused move touched the folder")
	}
	if err := a.rename([]string{"work", "client"}, true); err == nil {
		t.Error("--move without a terminal must be refused")
	}
}

func TestMoveAccount(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	logoutLog := filepath.Join(dir, "logout")
	fake := filepath.Join(dir, "claude")
	mustWrite(t, fake, "#!/bin/sh\necho \"$CLAUDE_CONFIG_DIR $*\" >> "+logoutLog+"\n")
	must(t, os.Chmod(fake, 0o755))
	f.cfg.Default = "work"
	a := &app{p: paths{home: f.home, config: filepath.Join(dir, "config.toml"), last: filepath.Join(dir, "last")}, cfg: f.cfg, claude: fake}
	before := snapshot(t, f.root)

	work, _ := f.cfg.find("work")
	to := moveTarget(f.cfg.dir(work, f.home), "client")
	moved, err := a.moveAccount(work, "client", to, true)
	must(t, err)

	if b, _ := os.ReadFile(logoutLog); !strings.Contains(string(b), filepath.Join(f.home, ".claude-work")+" auth logout") {
		t.Errorf("must log out at the old path first, got %q", b)
	}
	if moved.Name != "client" || moved.Dir != "~/.claude-client" || a.cfg.Default != "client" {
		t.Errorf("moved = %+v, default %q", moved, a.cfg.Default)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".claude-work")); err == nil {
		t.Error("old folder still there")
	}
	if b, err := os.ReadFile(filepath.Join(to, "projects", "p", "work-session.jsonl")); err != nil || string(b) != "work" {
		t.Error("sessions did not move with the folder")
	}
	if b, err := os.ReadFile(filepath.Join(to, "skills", "s", "SKILL.md")); err != nil || string(b) != "skill" {
		t.Error("shared links broke after the move")
	}
	if b, _ := os.ReadFile(filepath.Join(to, markerFile)); strings.TrimSpace(string(b)) != "client" {
		t.Errorf("marker = %q", b)
	}
	assertSnapshot(t, f.root, before)
}

func TestMoveAccountWithoutLogout(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	fake := filepath.Join(dir, "claude")
	mustWrite(t, fake, "#!/bin/sh\necho \"$*\" >> "+calls+"\n")
	must(t, os.Chmod(fake, 0o755))
	mustWrite(t, filepath.Join(f.home, ".claude-work", ".credentials.json"), "secret")
	a := &app{p: paths{home: f.home, config: filepath.Join(dir, "config.toml"), last: filepath.Join(dir, "last")}, cfg: f.cfg, claude: fake}

	work, _ := f.cfg.find("work")
	to := moveTarget(f.cfg.dir(work, f.home), "client")
	_, err := a.moveAccount(work, "client", to, false)
	must(t, err)
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("Linux/Windows move must not call claude, got %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(to, ".credentials.json")); string(b) != "secret" {
		t.Error("the credentials file must move with the folder")
	}
}

func TestIsLinkCoversWindowsJunctions(t *testing.T) {
	if !isLink(fs.ModeSymlink) || !isLink(fs.ModeIrregular) || isLink(fs.ModeDir) || isLink(0) {
		t.Error("isLink must cover symlinks and junctions (ModeIrregular) only")
	}
}

func TestClaudeEnvStripsOnlyOurVars(t *testing.T) {
	cfg := &Config{Root: "~/.claude"}
	env := claudeEnv([]string{"CLAUDE_CONFIG_DIR=/x", "CSW_ACCOUNT=old", "CLAUDE_CONFIG_DIRX=keep", "PATH=/bin"}, cfg, Account{Name: "work", Dir: "/tmp/w"}, "/home/u")
	got := strings.Join(env, " ")
	for _, want := range []string{"CLAUDE_CONFIG_DIRX=keep", "PATH=/bin", "CSW_ACCOUNT=work", "CLAUDE_CONFIG_DIR=/tmp/w"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "CSW_ACCOUNT=old") || strings.Contains(got, "CLAUDE_CONFIG_DIR=/x") {
		t.Errorf("inherited values kept: %q", got)
	}
}

func TestTypoOfCaseOnly(t *testing.T) {
	if got := typoOf("Work", []string{"work"}); got != "work" {
		t.Errorf("case-only difference: got %q", got)
	}
	if got := typoOf("work", []string{"work"}); got != "" {
		t.Errorf("exact match is not a typo: got %q", got)
	}
}

func TestVersionFrom(t *testing.T) {
	cases := []struct{ ldflag, module, want string }{
		{"0.1.0", "v0.2.0", "0.1.0"}, // build flag wins (GoReleaser, Homebrew)
		{"dev", "v0.1.0", "0.1.0"},   // go install module@v0.1.0
		{"dev", "(devel)", "dev"},    // local go build
		{"dev", "", "dev"},
		{"dev", "v0.1.1-0.20261023-abcdef", "0.1.1-0.20261023-abcdef"},
	}
	for _, c := range cases {
		if got := versionFrom(c.ldflag, c.module); got != c.want {
			t.Errorf("versionFrom(%q, %q) = %q, want %q", c.ldflag, c.module, got, c.want)
		}
	}
}

func TestShownPathOnWindows(t *testing.T) {
	cases := []struct{ in, unix, windows string }{
		{"~/.claude", "~/.claude", `%USERPROFILE%\.claude`},
		{"~/.claude/settings.json", "~/.claude/settings.json", `%USERPROFILE%\.claude\settings.json`},
		{"~/accounts/acme", "~/accounts/acme", `%USERPROFILE%\accounts\acme`},
		{`D:\claude\work`, `D:\claude\work`, `D:\claude\work`},
	}
	for _, c := range cases {
		if got := shownPathFor(c.in, false); got != c.unix {
			t.Errorf("unix %q: got %q", c.in, got)
		}
		if got := shownPathFor(c.in, true); got != c.windows {
			t.Errorf("windows %q: got %q, want %q", c.in, got, c.windows)
		}
	}
}

func TestExpandHomeTakesUserProfile(t *testing.T) {
	home := "/home/u"
	for _, in := range []string{"%USERPROFILE%/.claude", "%userprofile%/.claude", "~/.claude"} {
		if got := expandHome(in, home); got != filepath.Join(home, ".claude") {
			t.Errorf("expandHome(%q) = %q", in, got)
		}
	}
	if got := expandHome("%USERPROFILE%", home); got != home {
		t.Errorf("bare %%USERPROFILE%% = %q", got)
	}
}
