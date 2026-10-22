package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
)

// onboard runs on first use. Without a terminal it silently registers the
// current Claude directory so scripts keep working.
func onboard(p paths) (*app, error) {
	root := detectRoot(p.home)
	a := &app{p: p, cfg: newConfig(root, p.home)}
	if !interactive() {
		a.cfg.Accounts = []Account{{Name: "default", Dir: a.cfg.Root}}
		claude, _, _ := findClaude(p.home)
		a.claude = claude
		return a, a.save()
	}

	fmt.Fprintln(os.Stderr, nameSty.Render("csw")+" — switch between your Claude subscriptions\n")
	claude, err := ensureClaude(p.home, true)
	if err != nil {
		return nil, err
	}
	a.claude = claude

	rootInput, err := askRoot(a.cfg.Root, p.home)
	if err != nil {
		return nil, err
	}
	a.cfg = newConfig(rootInput, p.home)

	rootAcct := Account{Dir: a.cfg.Root}
	var st authStatus
	withSpinner("Checking your current login…", func() { st = fetchStatus(claude, a.env(rootAcct)) })
	title := "Name this account"
	if st.LoggedIn {
		title = fmt.Sprintf("Found %s (%s). Name this account", st.Email, st.SubscriptionType)
	}
	rootAcct.Name, err = askName(title, "personal", func(string) bool { return false })
	if err != nil {
		return nil, err
	}
	rootAcct.Email = st.Email
	a.cfg.Accounts = []Account{rootAcct}
	if err := a.save(); err != nil {
		return nil, err
	}
	if !st.LoggedIn {
		if _, err := a.login(rootAcct); err != nil {
			warn(err.Error())
		}
	}

	for {
		more, err := confirm("Add another account?", "Each account gets its own folder and login; settings, plugins and skills stay shared.", len(a.cfg.Accounts) == 1)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
		placeholder := "work"
		if _, taken := a.cfg.find(placeholder); taken {
			placeholder = "client"
		}
		name, err := askName("Account name", placeholder, func(s string) bool { _, ok := a.cfg.find(s); return ok })
		if err != nil {
			return nil, err
		}
		if _, err := a.addAccount(name, "", true); err != nil {
			warn(err.Error())
		}
	}

	install, err := confirm("Show the account and email in Claude's status line?",
		"Edits "+a.cfg.Root+"/settings.json (a backup is kept). An existing status line keeps working.", true)
	if err != nil {
		return nil, err
	}
	if install {
		if err := a.installStatusline(); err != nil {
			warn(err.Error())
		}
	}

	done("setup complete. Run %s any time to pick an account; %s shows them all.", nameSty.Render("csw"), nameSty.Render("csw status"))
	return a, nil
}

func askRoot(detected, home string) (string, error) {
	keep, err := confirm("Is "+detected+" your main Claude folder?",
		"Your current login lives here. csw never deletes this folder or anything in it.", true)
	if err != nil || keep {
		return detected, err
	}
	var custom string
	err = huh.NewInput().
		Title("Your main Claude folder").
		Description("csw never deletes this folder or anything in it.").
		Placeholder(detected).
		Value(&custom).
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("enter a folder path")
			}
			if info, err := os.Stat(expandHome(strings.TrimSpace(s), home)); err != nil || !info.IsDir() {
				return errors.New("folder not found")
			}
			return nil
		}).
		WithTheme(theme()).
		Run()
	return strings.TrimSpace(custom), err
}

func (a *app) installStatusline() error {
	ch, err := installStatusline(a.cfg, a.p.home)
	if err != nil {
		return err
	}
	if !ch.Installed {
		done("status line already installed")
		return nil
	}
	if err := a.save(); err != nil {
		return err
	}
	done("status line installed")
	if ch.Wrapped != "" {
		hint("your previous status line still runs: " + ch.Wrapped)
	}
	if ch.Backup != "" {
		hint("backup: " + ch.Backup)
	}
	return nil
}

func (a *app) uninstallStatusline() error {
	ch, err := uninstallStatusline(a.cfg, a.p.home)
	if err != nil {
		return err
	}
	if !ch.Installed {
		done("status line wasn't installed")
		return nil
	}
	if err := a.save(); err != nil {
		return err
	}
	if ch.Wrapped != "" {
		done("restored your previous status line: %s", ch.Wrapped)
	} else {
		done("status line removed")
	}
	return nil
}
