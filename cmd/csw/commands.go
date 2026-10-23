package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
)

type app struct {
	p      paths
	cfg    *Config
	claude string
}

func (a *app) save() error { return saveConfig(a.p.config, a.cfg) }

func (a *app) env(acct Account) []string {
	return claudeEnv(os.Environ(), a.cfg, acct, a.p.home)
}

func (a *app) launch(cmd command) error {
	acct, fromDefault, err := a.chooseAccount(cmd.account, cmd.args)
	if err != nil {
		return err
	}
	if err := saveLastUsed(a.p.last, acct.Name); err != nil {
		hint("csw: couldn't remember the last account: " + err.Error())
	}
	if cmd.save && a.cfg.Default != acct.Name {
		a.cfg.Default = acct.Name
		if err := a.save(); err != nil {
			return err
		}
		done("%s is now the default; csw starts it without asking (undo: csw --clear)", acct.Name)
	}
	a.syncShared(acct)
	for _, msg := range a.loginOverrides() {
		warn(msg)
	}
	announce(acct, fromDefault && len(a.cfg.Accounts) > 1)
	return execClaude(a.claude, cmd.args, a.env(acct))
}

// syncShared links root items created since the account was added, so a
// new agent or skill in the root shows up everywhere. It never replaces
// anything and never blocks a launch.
func (a *app) syncShared(acct Account) {
	if a.cfg.isRoot(acct, a.p.home) {
		return
	}
	dir := a.cfg.dir(acct, a.p.home)
	if _, err := os.Stat(dir); err != nil || checkAccountDir(dir, a.cfg, a.p.home, acct.Name) != nil {
		return
	}
	_, _ = linkShared(a.cfg.rootDir(a.p.home), dir, a.cfg.Shared)
}

// claudeCommandLine shows what will run after the pick, so a prompt never
// starts unnoticed.
func claudeCommandLine(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = arg
		if arg == "" || strings.ContainsAny(arg, " \t\n'\"") {
			quoted[i] = strconv.Quote(arg)
		}
	}
	return "then: claude " + strings.Join(quoted, " ")
}

// chooseAccount resolves, in order: -a / csw <name>, CSW_ACCOUNT, the saved
// default, the only account, the picker.
func (a *app) chooseAccount(name string, claudeArgs []string) (acct Account, fromDefault bool, err error) {
	if name == "" {
		name = os.Getenv(accountEnv)
	}
	if name != "" {
		acct, err = a.cfg.lookup(name)
		return acct, false, err
	}
	if acct, ok := a.cfg.find(a.cfg.Default); ok {
		return acct, true, nil
	}
	switch {
	case len(a.cfg.Accounts) == 0:
		return Account{}, false, errors.New("no accounts; run csw add <name>")
	case len(a.cfg.Accounts) == 1:
		return a.cfg.Accounts[0], false, nil
	case !interactive():
		return Account{}, false, errors.New("not a terminal; pass an account (csw work), set CSW_ACCOUNT, or save a default: csw work --save")
	}
	cursor := cmp.Or(a.cfg.Default, loadLastUsed(a.p.last))
	name, err = pickAccount("Which account?", claudeCommandLine(claudeArgs), a.cfg.Accounts, a.p, cursor, a.cfg.Default)
	if err != nil {
		return Account{}, false, err
	}
	acct, err = a.cfg.lookup(name)
	return acct, false, err
}

// addAccount creates (or adopts) an account directory, links the shared root
// items into it and logs in. Only directories csw creates get the marker
// that makes them purgeable later.
func (a *app) addAccount(name, dir string, login bool) (Account, error) {
	if err := checkName(name); err != nil {
		return Account{}, err
	}
	if _, ok := a.cfg.find(name); ok {
		return Account{}, fmt.Errorf("account %q already exists", name)
	}
	if dir == "" {
		dir = "~/.claude-" + name
	}
	abs := expandHome(dir, a.p.home)
	if err := checkAccountDir(abs, a.cfg, a.p.home, name); err != nil {
		return Account{}, err
	}
	_, statErr := os.Stat(abs)
	created := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return Account{}, err
	}
	if created {
		if err := writeMarker(abs, name); err != nil {
			return Account{}, err
		}
	}

	res, err := linkShared(a.cfg.rootDir(a.p.home), abs, a.cfg.Shared)
	if err != nil {
		return Account{}, err
	}
	if len(res.Linked) > 0 {
		hint("shared from " + shownPath(a.cfg.Root) + ": " + strings.Join(res.Linked, ", "))
	}
	if len(res.Conflicts) > 0 {
		warn("kept existing, not shared: " + strings.Join(res.Conflicts, ", "))
	}

	acct := Account{Name: name, Dir: abbreviateHome(abs, a.p.home)}
	a.cfg.Accounts = append(a.cfg.Accounts, acct)
	if err := a.save(); err != nil {
		return Account{}, err
	}
	done("added %s (%s)", nameSty.Render(name), shownPath(acct.Dir))
	if !login {
		return acct, nil
	}
	return a.login(acct)
}

func (a *app) login(acct Account) (Account, error) {
	fmt.Fprintf(os.Stderr, "Log in to the account for %s in the browser window that opens.\n", nameSty.Render(acct.Name))
	if err := runInteractive(a.claude, a.env(acct), "auth", "login"); err != nil {
		return acct, fmt.Errorf("login failed (%w); retry with: csw -a %s auth login", err, acct.Name)
	}
	if st := fetchStatus(a.claude, a.env(acct)); st.LoggedIn {
		acct.Email = st.Email
		a.cfg.setEmail(acct.Name, st.Email)
		if err := a.save(); err != nil {
			return acct, err
		}
		done("%s is logged in as %s", nameSty.Render(acct.Name), st.Email)
	}
	return acct, nil
}

type removeOpts struct {
	purge    bool
	sessions string
	dryRun   bool
	force    bool
}

const (
	keepArchive = "archive"
	keepMove    = "move"
)

// rename changes the name csw uses. With move it also renames the folder;
// Claude keys an account's login to its folder path, so that needs a fresh
// login, which the user confirms first.
func (a *app) rename(args []string, move bool) error {
	var oldName, newName string
	if len(args) > 0 {
		oldName = args[0]
	}
	if len(args) > 1 {
		newName = args[1]
	}
	if (oldName == "" || newName == "" || move) && !interactive() {
		if move {
			return errors.New("not a terminal; --move needs one to confirm and log in again")
		}
		return errors.New("not a terminal; pass both names: csw rename <old> <new>")
	}
	var err error
	if oldName == "" {
		if oldName, err = pickAccount("Rename which account?", "", a.cfg.Accounts, a.p, "", a.cfg.Default); err != nil {
			return err
		}
	}
	acct, err := a.cfg.lookup(oldName)
	if err != nil {
		return err
	}
	taken := func(s string) bool { _, ok := a.cfg.find(s); return ok }
	if newName == "" {
		if newName, err = askName("New name for "+acct.Name, "client", taken); err != nil {
			return err
		}
	}
	if err := checkName(newName); err != nil {
		return err
	}
	if taken(newName) {
		return fmt.Errorf("account %q already exists", newName)
	}
	if move {
		return a.renameAndMove(acct, newName)
	}

	a.applyRename(acct, newName, acct.Dir)
	if err := a.save(); err != nil {
		return err
	}
	done("renamed %s to %s (folder unchanged: %s; --move renames it too)", acct.Name, newName, shownPath(acct.Dir))
	return nil
}

func (a *app) applyRename(acct Account, newName, newDir string) {
	a.cfg.rename(acct.Name, newName)
	for i := range a.cfg.Accounts {
		if a.cfg.Accounts[i].Name == newName {
			a.cfg.Accounts[i].Dir = newDir
		}
	}
	if loadLastUsed(a.p.last) == acct.Name {
		_ = saveLastUsed(a.p.last, newName)
	}
	marker := filepath.Join(expandHome(newDir, a.p.home), markerFile)
	if _, err := os.Stat(marker); err == nil {
		_ = writeMarker(filepath.Dir(marker), newName)
	}
}

// moveTarget is the folder an account moves to: a sibling named like the
// one csw add creates.
func moveTarget(dir, newName string) string {
	return filepath.Join(filepath.Dir(dir), ".claude-"+newName)
}

func (a *app) renameAndMove(acct Account, newName string) error {
	if a.cfg.isRoot(acct, a.p.home) {
		return fmt.Errorf("%s uses the root folder %s, which can't move; rename it without --move", acct.Name, shownPath(a.cfg.Root))
	}
	if a.claude == "" {
		return errClaudeMissing
	}
	from := a.cfg.dir(acct, a.p.home)
	to := moveTarget(from, newName)
	if _, err := os.Stat(from); err != nil {
		return fmt.Errorf("%s is missing; rename without --move", shownPath(acct.Dir))
	}
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("%s already exists", shownPath(abbreviateHome(to, a.p.home)))
	}
	if err := checkAccountDir(to, a.cfg, a.p.home, acct.Name); err != nil {
		return err
	}
	shownTo := abbreviateHome(to, a.p.home)

	login := "The login is a file in the folder, so it moves too."
	if loginKeyedToPath {
		login = fmt.Sprintf("Claude ties the login to the folder, so %s is logged out and you log in again in your browser.", acct.Name)
	}
	ok, err := confirm(fmt.Sprintf("Rename %s to %s and move its folder?", acct.Name, newName),
		fmt.Sprintf("Moves %s to %s. Sessions and settings move with it.\n%s\nClose any open %s sessions first.",
			shownPath(acct.Dir), shownPath(shownTo), login, acct.Name), false)
	if err != nil {
		return err
	}
	if !ok {
		hint("nothing changed")
		return nil
	}

	moved, err := a.moveAccount(acct, newName, to, loginKeyedToPath)
	if err != nil {
		return err
	}
	done("renamed %s to %s and moved its folder to %s", acct.Name, newName, shownPath(moved.Dir))
	if st := fetchStatus(a.claude, a.env(moved)); st.LoggedIn {
		done("%s is still logged in as %s", newName, st.Email)
		return nil
	}
	if _, err := a.login(moved); err != nil {
		warn(err.Error())
	}
	return nil
}

// loginKeyedToPath: on macOS the login lives in a Keychain entry named after
// the config folder's path; on Linux and Windows it's .credentials.json
// inside the folder (code.claude.com/docs/en/authentication).
const loginKeyedToPath = runtime.GOOS == "darwin"

func (a *app) moveAccount(acct Account, newName, to string, logoutFirst bool) (Account, error) {
	// On macOS, log out first so the old path's Keychain entry isn't left behind.
	if logoutFirst {
		if err := logout(a.claude, a.env(acct)); err != nil {
			warn(err.Error())
		}
	}
	if err := os.Rename(a.cfg.dir(acct, a.p.home), to); err != nil {
		if logoutFirst {
			return acct, fmt.Errorf("moving %s: %w (%s is logged out; log back in: csw %s auth login)", shownPath(acct.Dir), err, acct.Name, acct.Name)
		}
		return acct, fmt.Errorf("moving %s: %w (nothing changed)", shownPath(acct.Dir), err)
	}
	shownTo := abbreviateHome(to, a.p.home)
	a.applyRename(acct, newName, shownTo)
	if err := a.save(); err != nil {
		return acct, fmt.Errorf("%w; the folder is now %s, fix dir for %s in %s", err, shownPath(shownTo), acct.Name, a.p.config)
	}
	moved, _ := a.cfg.find(newName)
	return moved, nil
}

// chooseRemovable asks which account to remove when none was named.
func (a *app) chooseRemovable() (string, error) {
	var removable []Account
	for _, acct := range a.cfg.Accounts {
		if !a.cfg.isRoot(acct, a.p.home) {
			removable = append(removable, acct)
		}
	}
	if len(removable) == 0 {
		return "", fmt.Errorf("no accounts to remove; %s is the root account and can't be removed", a.rootAccount().Name)
	}
	names := make([]string, len(removable))
	for i, acct := range removable {
		names[i] = acct.Name
	}
	if !interactive() {
		return "", fmt.Errorf("remove needs an account name: %s", strings.Join(names, " | "))
	}
	return pickAccount("Remove which account?", "", removable, a.p, "", a.cfg.Default)
}

func (a *app) remove(name string, o removeOpts) error {
	acct, err := a.cfg.lookup(name)
	if err != nil {
		return err
	}
	if a.cfg.isRoot(acct, a.p.home) {
		return fmt.Errorf("%s is the root account (%s) and can't be removed", acct.Name, shownPath(a.cfg.Root))
	}
	root := a.rootAccount()

	if !o.purge && o.sessions == "" {
		if !interactive() {
			return errors.New("choose what happens to the data: --purge, or --sessions archive|move")
		}
		_, purgeErr := checkPurgeable(acct, a.cfg, a.p.home)
		if o.purge, o.sessions, err = askRemoveMode(acct, root, purgeErr); err != nil {
			return err
		}
	}
	if o.sessions != "" && o.sessions != keepArchive && o.sessions != keepMove {
		return fmt.Errorf("--sessions must be %q or %q", keepArchive, keepMove)
	}

	var purgePath string
	if o.purge || o.sessions == keepMove {
		purgePath, err = checkPurgeable(acct, a.cfg, a.p.home)
		if err != nil && o.purge {
			return err
		}
		if err != nil {
			warn("the folder will be kept: " + err.Error())
			purgePath = ""
		}
	}

	steps := a.removePlan(acct, root, o, purgePath)
	fmt.Fprintln(os.Stderr, nameSty.Render("Removing "+acct.Name)+dimSty.Render("  "+shownPath(acct.Dir)))
	for _, s := range steps {
		fmt.Fprintln(os.Stderr, "  • "+s)
	}
	if o.dryRun {
		hint("dry run: nothing changed")
		return nil
	}
	if err := confirmRemove(acct, o); err != nil {
		return err
	}

	if err := logout(a.claude, a.env(acct)); err != nil {
		warn(err.Error())
	}
	if o.sessions == keepMove {
		moved, conflicts, err := moveSessions(
			filepath.Join(a.cfg.dir(acct, a.p.home), "projects"),
			filepath.Join(a.cfg.rootDir(a.p.home), "projects"))
		if err != nil {
			return fmt.Errorf("moving sessions: %w (account kept)", err)
		}
		done("moved %d session file(s) into %s", moved, root.Name)
		if conflicts > 0 {
			warn(fmt.Sprintf("%d file(s) already existed in %s and were not moved; they stay with the rest of the folder", conflicts, root.Name))
		}
	}
	if purgePath != "" {
		if err := deleteAccountDir(a.cfg.rootDir(a.p.home), purgePath, a.cfg.Shared); err != nil {
			return err
		}
		done("deleted %s", shownPath(acct.Dir))
	}

	a.cfg.remove(acct.Name)
	if err := a.save(); err != nil {
		return err
	}
	if loadLastUsed(a.p.last) == acct.Name {
		_ = os.Remove(a.p.last)
	}
	done("removed %s", nameSty.Render(acct.Name))
	if o.sessions == keepArchive {
		hint(fmt.Sprintf("restore later with: csw add %s --dir %s", acct.Name, shownPath(acct.Dir)))
	}
	return nil
}

func (a *app) rootAccount() Account {
	for _, acct := range a.cfg.Accounts {
		if a.cfg.isRoot(acct, a.p.home) {
			return acct
		}
	}
	return Account{Name: "root", Dir: a.cfg.Root}
}

func (a *app) removePlan(acct, root Account, o removeOpts, purgePath string) []string {
	steps := []string{"log out " + acct.Name + " (only this account)"}
	switch {
	case o.purge:
		steps = append(steps, "permanently delete "+shownPath(acct.Dir)+" (sessions, history, settings)")
	case o.sessions == keepMove:
		steps = append(steps, "move its sessions into "+root.Name+" ("+shownPath(a.cfg.Root+"/projects")+"), never overwriting")
		if purgePath != "" {
			steps = append(steps, "permanently delete the rest of "+shownPath(acct.Dir))
		}
	default:
		steps = append(steps, "keep "+shownPath(acct.Dir)+" untouched")
	}
	return append(steps, "forget "+acct.Name+" in csw", "never touch "+shownPath(a.cfg.Root))
}

// askRemoveMode hides the delete options when the folder can't be deleted, so
// the user never picks a choice that is then refused.
func askRemoveMode(acct, root Account, purgeErr error) (purge bool, sessions string, err error) {
	choice := keepArchive
	opts := []huh.Option[string]{
		huh.NewOption("Keep sessions in "+shownPath(acct.Dir)+"  "+dimSty.Render("nothing is deleted; restore any time"), keepArchive),
	}
	description := ""
	if purgeErr == nil {
		opts = append(opts,
			huh.NewOption("Move sessions into "+root.Name+"  "+dimSty.Render("resumable there; the rest is deleted"), keepMove),
			huh.NewOption("Delete everything  "+dimSty.Render("permanently, no undo"), "purge"))
	} else {
		opts = append(opts,
			huh.NewOption("Move sessions into "+root.Name+"  "+dimSty.Render("resumable there; the folder is kept"), keepMove))
		description = purgeErr.Error()
	}
	err = huh.NewSelect[string]().
		Title("Remove " + acct.Name + ": what happens to its data?").
		Description(description).
		Options(opts...).
		Value(&choice).
		WithTheme(theme()).
		Run()
	if choice == "purge" {
		return true, "", err
	}
	return false, choice, err
}

func confirmRemove(acct Account, o removeOpts) error {
	if o.force {
		return nil
	}
	if !interactive() {
		return errors.New("not a terminal; pass --force to confirm")
	}
	if !o.purge {
		ok, err := confirm("Remove "+acct.Name+"?", "", false)
		if err != nil {
			return err
		}
		if !ok {
			return huh.ErrUserAborted
		}
		return nil
	}
	var typed string
	return huh.NewInput().
		Title("Type " + acct.Name + " to permanently delete " + shownPath(acct.Dir)).
		Value(&typed).
		Validate(func(s string) error {
			if s != acct.Name {
				return fmt.Errorf("type %q exactly", acct.Name)
			}
			return nil
		}).
		WithTheme(theme()).
		Run()
}

// moveSessions moves files from src into dst without overwriting anything and
// without following symlinks. When src is a link to dst (projects shared),
// there is nothing to move.
func moveSessions(src, dst string) (moved, conflicts int, err error) {
	info, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if isLink(info.Mode()) || samePath(src, dst) {
		return 0, 0, nil
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || isLink(d.Type()) {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if _, err := os.Lstat(target); err == nil {
			conflicts++
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.Rename(path, target); err != nil {
			return err
		}
		moved++
		return nil
	})
	return moved, conflicts, err
}

// loginOverrides finds settings that make Claude ignore the account's
// login, which would silently turn switching into a no-op.
func (a *app) loginOverrides() []string {
	var out []string
	for _, v := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if os.Getenv(v) != "" {
			out = append(out, v+" is set, so every account uses it instead of its login; unset it in your shell profile")
		}
	}
	settings := filepath.Join(a.cfg.rootDir(a.p.home), "settings.json")
	if members, err := readSettings(settings); err == nil {
		for _, m := range members {
			if m.key == "apiKeyHelper" {
				out = append(out, "apiKeyHelper in "+settings+" makes every account use it instead of its login")
			}
		}
	}
	return out
}
