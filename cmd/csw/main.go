package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/mattn/go-isatty"
)

// version is set at build time (-ldflags "-X main.version=0.1.0") by
// GoReleaser and the Homebrew formula. `go install` sets no flags, so
// cswVersion falls back to the module version Go records in the binary.
var version = "dev"

func cswVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	return versionFrom(version, info.Main.Version)
}

func versionFrom(ldflag, module string) string {
	if ldflag != "dev" || module == "" || module == "(devel)" {
		return ldflag
	}
	return strings.TrimPrefix(module, "v")
}

type subcommand struct {
	name    string
	summary string
	usage   string
}

var subcommands = []subcommand{
	{"add", "add an account", `Usage: csw add <name> [--dir <path>] [--no-login]

Creates the account's folder (default ~/.claude-<name>), shares settings,
plugins and skills from the root folder, then opens the login.

Examples:
  csw add work
  csw add client --dir ~/accounts/client`},
	{"rename", "rename an account", `Usage: csw rename [old] [new] [--move]

Changes the name csw uses for an account. Its folder, login and sessions
stay as they are. Asks for whatever you leave out.

  --move  also rename the folder (~/.claude-work to ~/.claude-client).
          On macOS Claude ties the login to the folder, so you log in
          again; on Linux and Windows the login moves with it. csw asks
          before doing anything. Not for the root account.

Examples:
  csw rename work client
  csw rename work client --move
  csw rename`},
	{"remove", "remove an account", `Usage: csw remove [name] [--sessions archive|move | --purge] [-n] [-f]

Logs the account out and forgets it. Without a name, asks which one. You choose what happens to its data:
  --sessions archive  keep its folder untouched (restore with csw add --dir)
  --sessions move     move its sessions into the root account, delete the rest
  --purge             permanently delete the whole folder

Deleting is permanent. The root folder is never touched, and only folders
csw created can be deleted.

Flags:
  -n, --dry-run  show what would happen
  -f, --force    don't ask for confirmation (required without a terminal)

Examples:
  csw remove work
  csw remove work --purge --dry-run`},
	{"status", "show accounts and logins", `Usage: csw status [--json | --plain]

Shows every account's email, plan and login state.`},
	{"statusline", "manage the Claude status line", `Usage: csw statusline [install | uninstall | colors [account] [--reset]]

install    show the account and its email in Claude Code's status line
           (edits the root settings.json, keeps a backup and keeps any
           existing status line running next to it)
uninstall  restore the previous status line
colors     pick colors for the name and email, with a live preview
           (#RRGGBB, #RGB or a terminal color 0-255). With an account name,
           sets that account's own colors; --reset clears them

Without arguments, shows whether it's installed. (Claude Code itself runs
csw statusline with JSON on stdin to draw the line.)`},
	{"version", "show csw and Claude Code versions", "Usage: csw version"},
	{"help", "show help", "Usage: csw help [command]"},
}

// claudeSubcommands are never treated as typos of csw commands.
var claudeSubcommands = []string{"agents", "attach", "auth", "auto-mode", "doctor", "gateway", "import",
	"install", "logs", "mcp", "plugin", "purge", "respawn", "rm", "setup-token", "stop", "ultrareview", "update"}

const usage = `csw — switch between your Claude subscriptions

Usage:
  csw [claude args...]               pick an account (or start the default)
  csw <account> [claude args...]     start claude on <account> (same as -a)
  csw <account> --save [claude args] start it and make it the default
  csw --clear                        forget the default; csw shows the picker
  csw <command> [flags]

Examples:
  csw                      pick an account and start claude
  csw work                 start claude on "work"
  csw work --save          start "work" and make it the default (no picker)
  csw work --resume        pick one of work's past conversations to resume
  csw work -c              continue work's latest conversation in this folder
  csw add work             add and log in to a new account
  csw status               see every account and its login

Commands:
%s
Quote prompts: csw work "fix the bug". Several unquoted words are refused,
since claude would start working on them. A word one keystroke from a
command or account is treated as a typo; put it after -- to send it anyway.
CSW_ACCOUNT=<name> works like -a.

Config: %s
`

func statuslineState(cfg *Config, cfgErr error, p paths) error {
	if cfgErr != nil {
		return cfgErr
	}
	if statuslineInstalled(cfg, p.home) {
		done("status line installed: Claude shows the account and email")
		hint("remove it: csw statusline uninstall")
	} else {
		fmt.Fprintln(os.Stderr, "status line not installed")
		hint("show the account and email in Claude: csw statusline install")
	}
	return nil
}

func subcommandNames() []string {
	names := make([]string, len(subcommands))
	for i, c := range subcommands {
		names[i] = c.name
	}
	return names
}

func findSubcommand(name string) (subcommand, bool) {
	for _, c := range subcommands {
		if c.name == name {
			return c, true
		}
	}
	return subcommand{}, false
}

func printUsage(p paths) {
	var b strings.Builder
	for _, c := range subcommands {
		fmt.Fprintf(&b, "  %-11s %s\n", c.name, c.summary)
	}
	fmt.Printf(usage, b.String(), p.config)
}

type command struct {
	kind    string
	account string
	args    []string
	// literal is set by a leading --: args go to claude untouched.
	literal bool
	// save makes the chosen account the default (csw work --save).
	save bool
}

func parseArgs(args []string) (command, error) {
	if len(args) == 0 {
		return command{kind: "launch"}, nil
	}
	first := args[0]
	switch {
	case first == "-h" || first == "--help":
		return command{kind: "help"}, nil
	case first == "--version":
		return command{kind: "version"}, nil
	case first == "--":
		return launchCmd("", args), nil
	case first == "-a" || first == "--account":
		if len(args) < 2 {
			return command{}, errMissingAccount
		}
		return launchCmd(args[1], args[2:]), nil
	case strings.HasPrefix(first, "--account="):
		return launchCmd(strings.TrimPrefix(first, "--account="), args[1:]), nil
	case first == "--clear":
		if len(args) > 1 {
			return command{}, errors.New("--clear takes nothing else: csw --clear")
		}
		return command{kind: "clear-default"}, nil
	case first == "--save":
		return launchCmd("", args), nil
	}
	if _, ok := findSubcommand(first); ok {
		return command{kind: first, args: args[1:]}, nil
	}
	return command{kind: "launch", args: args}, nil
}

// launchCmd reads csw's own launch flag, which only counts right after the
// account (or first), so claude args that follow are never taken over.
func launchCmd(account string, rest []string) command {
	cmd := command{kind: "launch", account: account}
	if len(rest) > 0 && rest[0] == "--save" {
		cmd.save, rest = true, rest[1:]
	}
	if len(rest) > 0 && rest[0] == "--" {
		cmd.literal, rest = true, rest[1:]
	}
	cmd.args = rest
	return cmd
}

type flagSpec struct {
	names    []string
	hasValue bool
}

var (
	errHelp           = errors.New("help requested")
	errMissingAccount = errors.New("-a needs an account name")
)

// parseFlags allows flags and positionals in any order: `remove work --purge`
// and `remove --purge work` both work.
func parseFlags(args []string, specs map[string]flagSpec) (map[string]string, []string, error) {
	flags := map[string]string{}
	var pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return nil, nil, errHelp
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			pos = append(pos, arg)
			continue
		}
		name, value, hasEq := strings.Cut(arg, "=")
		key, spec, ok := lookupFlag(specs, name)
		if !ok {
			return nil, nil, fmt.Errorf("unknown flag %s", name)
		}
		switch {
		case spec.hasValue && hasEq:
			flags[key] = value
		case spec.hasValue:
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("%s needs a value", name)
			}
			i++
			flags[key] = args[i]
		case hasEq:
			return nil, nil, fmt.Errorf("%s takes no value", name)
		default:
			flags[key] = "true"
		}
	}
	return flags, pos, nil
}

func lookupFlag(specs map[string]flagSpec, name string) (string, flagSpec, bool) {
	for key, s := range specs {
		if slices.Contains(s.names, name) {
			return key, s, true
		}
	}
	return "", flagSpec{}, false
}

func exactArgs(cmd string, pos []string, n int) error {
	if len(pos) != n {
		return fmt.Errorf("%s: expected %d argument(s), got %d; see csw %s --help", cmd, n, len(pos), cmd)
	}
	return nil
}

func main() {
	err := run(os.Args[1:])
	if errors.Is(err, huh.ErrUserAborted) {
		os.Exit(130)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, badSty.Render("csw: ")+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	p, err := resolvePaths()
	if err != nil {
		return err
	}
	cmd, err := parseArgs(args)
	if errors.Is(err, errMissingAccount) {
		if cfg, cfgErr := loadConfig(p.config); cfgErr == nil && len(cfg.Accounts) > 0 {
			return fmt.Errorf("%w: %s", err, strings.Join(cfg.names(), ", "))
		}
	}
	if err != nil {
		return err
	}

	switch cmd.kind {
	case "help":
		return help(p, cmd.args)
	case "version":
		return printVersion(p)
	case "statusline":
		if len(cmd.args) == 0 {
			cfg, err := loadConfig(p.config)
			// Claude pipes JSON in; a person typing the bare command gets
			// the state instead of a read that waits forever.
			if isatty.IsTerminal(os.Stdin.Fd()) {
				return statuslineState(cfg, err, p)
			}
			if err == nil {
				runStatusline(cfg, p, os.Stdin, os.Stdout)
			}
			return nil
		}
	}

	if slices.Contains(cmd.args, "-h") && cmd.kind != "launch" {
		return help(p, []string{cmd.kind})
	}

	a, err := loadApp(p, cmd.kind)
	if err != nil {
		return err
	}
	return a.dispatch(cmd)
}

func loadApp(p paths, kind string) (*app, error) {
	cfg, err := loadConfig(p.config)
	if errors.Is(err, errNoConfig) {
		return onboard(p)
	}
	if err != nil {
		return nil, err
	}
	a := &app{p: p, cfg: cfg}
	if cfg.colorsOutdated {
		if err := a.save(); err != nil {
			return nil, err
		}
	}
	switch kind {
	case "launch", "add":
		a.claude, err = ensureClaude(p.home, interactive())
	case "remove":
		a.claude, _, err = findClaude(p.home)
	default:
		a.claude, _, _ = findClaude(p.home)
	}
	return a, err
}

func (a *app) dispatch(cmd command) error {
	switch cmd.kind {
	case "launch":
		cmd = accountFromArgs(cmd, a.cfg)
		if err := a.checkUnquotedPrompt(cmd); err != nil {
			return err
		}
		return a.launch(cmd)

	case "clear-default":
		if a.cfg.Default == "" {
			done("no default account saved; csw shows the picker")
			return nil
		}
		a.cfg.Default = ""
		if err := a.save(); err != nil {
			return err
		}
		done("default cleared; csw shows the picker")
		return nil

	case "add":
		flags, pos, err := parseFlags(cmd.args, map[string]flagSpec{
			"dir":      {names: []string{"--dir"}, hasValue: true},
			"no-login": {names: []string{"--no-login"}},
		})
		if err != nil {
			return a.flagErr("add", err)
		}
		if err := exactArgs("add", pos, 1); err != nil {
			return err
		}
		if _, err := a.addAccount(pos[0], flags["dir"], flags["no-login"] == ""); err != nil {
			return err
		}
		hint("next: csw " + pos[0])
		return nil

	case "rename":
		flags, pos, err := parseFlags(cmd.args, map[string]flagSpec{"move": {names: []string{"--move"}}})
		if err != nil {
			return a.flagErr("rename", err)
		}
		if len(pos) > 2 {
			return exactArgs("rename", pos, 2)
		}
		return a.rename(pos, flags["move"] != "")

	case "remove":
		flags, pos, err := parseFlags(cmd.args, map[string]flagSpec{
			"purge":    {names: []string{"--purge"}},
			"sessions": {names: []string{"--sessions"}, hasValue: true},
			"dry-run":  {names: []string{"-n", "--dry-run"}},
			"force":    {names: []string{"-f", "--force"}},
		})
		if err != nil {
			return a.flagErr("remove", err)
		}
		if len(pos) > 1 {
			return exactArgs("remove", pos, 1)
		}
		if flags["purge"] != "" && flags["sessions"] != "" {
			return errors.New("--purge and --sessions are mutually exclusive")
		}
		if a.claude == "" {
			return errClaudeMissing
		}
		name := ""
		if len(pos) == 1 {
			name = pos[0]
		} else if name, err = a.chooseRemovable(); err != nil {
			return err
		}
		return a.remove(name, removeOpts{
			purge: flags["purge"] != "", sessions: flags["sessions"],
			dryRun: flags["dry-run"] != "", force: flags["force"] != "",
		})

	case "status":
		flags, pos, err := parseFlags(cmd.args, map[string]flagSpec{
			"json":  {names: []string{"--json"}},
			"plain": {names: []string{"--plain"}},
		})
		if err != nil {
			return a.flagErr("status", err)
		}
		if err := exactArgs("status", pos, 0); err != nil {
			return err
		}
		return a.status(flags["json"] != "", flags["plain"] != "")

	case "statusline":
		switch {
		case len(cmd.args) == 1 && cmd.args[0] == "install":
			return a.installStatusline()
		case len(cmd.args) == 1 && cmd.args[0] == "uninstall":
			return a.uninstallStatusline()
		case len(cmd.args) > 0 && cmd.args[0] == "colors":
			flags, pos, err := parseFlags(cmd.args[1:], map[string]flagSpec{"reset": {names: []string{"--reset"}}})
			if err != nil || len(pos) > 1 {
				return errors.New("usage: csw statusline colors [account] [--reset]")
			}
			return a.editColors(strings.Join(pos, ""), flags["reset"] != "")
		}
		return errors.New("usage: csw statusline [install | uninstall | colors [account] [--reset]]")
	}
	return fmt.Errorf("unknown command %q", cmd.kind)
}

// accountFromArgs turns `csw work …` into `csw -a work …`.
func accountFromArgs(cmd command, cfg *Config) command {
	if cmd.account != "" || cmd.literal || len(cmd.args) == 0 {
		return cmd
	}
	if _, ok := cfg.find(cmd.args[0]); ok {
		resolved := launchCmd(cmd.args[0], cmd.args[1:])
		resolved.save = resolved.save || cmd.save
		return resolved
	}
	return cmd
}

func (a *app) flagErr(cmd string, err error) error {
	if errors.Is(err, errHelp) {
		return help(a.p, []string{cmd})
	}
	return fmt.Errorf("%s: %w", cmd, err)
}

// strayWord returns the first claude argument when it is a bare word that
// claude wouldn't understand: not a flag, not a claude command, not a quoted
// prompt. `csw fix the bug` is almost always a typo or a missing account, and
// claude would start working on it as a prompt right away.
func strayWord(cmd command) string {
	if cmd.literal || len(cmd.args) == 0 {
		return ""
	}
	w := cmd.args[0]
	if strings.HasPrefix(w, "-") || strings.ContainsAny(w, " \t\n") || slices.Contains(claudeSubcommands, w) {
		return ""
	}
	return w
}

// strayCheck decides whether the claude args look like a mistake. One word
// is a prompt unless it's a near-typo of a command or account: quotes don't
// survive the shell, so `csw "hey"` and `csw hey` look the same. Several
// unquoted words are always refused.
func strayCheck(cmd command, names []string) (word, suggestion string) {
	word = strayWord(cmd)
	if word == "" {
		return "", ""
	}
	if cmd.account == "" {
		suggestion = typoOf(word, append(subcommandNames(), names...))
	}
	if len(cmd.args) == 1 && suggestion == "" {
		return "", ""
	}
	return word, suggestion
}

func (a *app) checkUnquotedPrompt(cmd command) error {
	word, suggestion := strayCheck(cmd, a.cfg.names())
	if word == "" {
		return nil
	}
	if suggestion != "" && interactive() {
		note := ""
		if len(cmd.args) == 1 {
			note = fmt.Sprintf("No sends %q to claude as the prompt.", word)
		}
		yes, err := confirm(fmt.Sprintf("Did you mean csw %s?", suggestion), note, true)
		if err != nil {
			return err
		}
		if !yes && len(cmd.args) == 1 {
			return nil
		}
		if yes {
			fixed := []string{suggestion}
			if cmd.save {
				fixed = append(fixed, "--save")
			}
			fixed = append(fixed, cmd.args[1:]...)
			if err := run(fixed); err != nil {
				return err
			}
			os.Exit(0)
		}
	}
	return strayWordError(cmd, word, suggestion, a.cfg.names())
}

func strayWordError(cmd command, word, suggestion string, accounts []string) error {
	prefix := "csw "
	if cmd.account != "" {
		prefix += cmd.account + " "
	}
	var b strings.Builder
	if cmd.account != "" {
		fmt.Fprintf(&b, "%q isn't a claude command", word)
	} else {
		fmt.Fprintf(&b, "%q isn't an account or command (accounts: %s)", word, strings.Join(accounts, ", "))
	}
	if suggestion != "" {
		fmt.Fprintf(&b, "\n  did you mean: csw %s", strings.Join(append([]string{suggestion}, cmd.args[1:]...), " "))
	}
	if len(cmd.args) > 1 {
		fmt.Fprintf(&b, "\n  to send claude a prompt, quote it: %s%q", prefix, strings.Join(cmd.args, " "))
	} else {
		fmt.Fprintf(&b, "\n  to send it to claude anyway: %s-- %s", prefix, word)
	}
	return errors.New(b.String())
}

func (a *app) status(asJSON, plain bool) error {
	var statuses []authStatus
	if a.claude == "" {
		statuses = make([]authStatus, len(a.cfg.Accounts))
		for i := range statuses {
			statuses[i].Err = errClaudeMissing
		}
	} else if asJSON || plain {
		statuses = fetchStatuses(a.claude, a.cfg, a.p.home)
	} else {
		withSpinner("Checking accounts…", func() { statuses = fetchStatuses(a.claude, a.cfg, a.p.home) })
	}
	if a.backfillEmails(statuses) {
		if err := a.save(); err != nil {
			return err
		}
	}
	views := buildViews(a.cfg, a.p, statuses)
	switch {
	case asJSON:
		return writeJSON(os.Stdout, views)
	case plain:
		writePlain(os.Stdout, views)
		return nil
	}
	fmt.Println(renderStatus(views))
	for _, h := range statusHints(views) {
		hint(h)
	}
	for _, msg := range a.loginOverrides() {
		warn(msg)
	}
	return nil
}

// backfillEmails records emails for accounts added before csw stored them,
// so the picker can show them without spawning claude.
func (a *app) backfillEmails(statuses []authStatus) bool {
	changed := false
	for i, st := range statuses {
		if st.LoggedIn && st.Email != "" && a.cfg.Accounts[i].Email == "" {
			a.cfg.Accounts[i].Email = st.Email
			changed = true
		}
	}
	return changed
}

func help(p paths, args []string) error {
	if len(args) == 0 {
		printUsage(p)
		return nil
	}
	c, ok := findSubcommand(args[0])
	if !ok {
		return fmt.Errorf("no command %q", args[0])
	}
	fmt.Println(c.usage)
	return nil
}

func printVersion(p paths) error {
	fmt.Println("csw " + cswVersion())
	if claude, _, err := findClaude(p.home); err == nil {
		fmt.Println("claude " + claudeVersion(claude))
	} else {
		fmt.Println("claude not installed")
	}
	return nil
}

func claudeVersion(claude string) string {
	out, err := exec.Command(claude, "--version").Output()
	if err != nil {
		return "unknown version"
	}
	return strings.TrimSpace(string(out))
}
