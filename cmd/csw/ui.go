package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/huh/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-isatty"
)

var (
	accent  = lipgloss.AdaptiveColor{Light: "#C15F3C", Dark: "#DA7756"}
	subtle  = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}
	good    = lipgloss.AdaptiveColor{Light: "#2E7D32", Dark: "#81C784"}
	caution = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#FFB74D"}
	bad     = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#E57373"}
	nameSty = lipgloss.NewStyle().Foreground(accent).Bold(true)
	dimSty  = lipgloss.NewStyle().Foreground(subtle)
	goodSty = lipgloss.NewStyle().Foreground(good)
	warnSty = lipgloss.NewStyle().Foreground(caution)
	badSty  = lipgloss.NewStyle().Foreground(bad)
)

func theme() *huh.Theme {
	t := huh.ThemeCharm()
	t.Focused.Title = t.Focused.Title.Foreground(accent)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(accent)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(accent)
	return t
}

func interactive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stderr.Fd())
}

func warn(msg string) { fmt.Fprintln(os.Stderr, warnSty.Render("! ")+msg) }
func hint(msg string) { fmt.Fprintln(os.Stderr, dimSty.Render(msg)) }
func done(format string, a ...any) {
	fmt.Fprintln(os.Stderr, goodSty.Render("✓ ")+fmt.Sprintf(format, a...))
}

func pickAccount(title, description string, accounts []Account, p paths, cursor, def string) (string, error) {
	choice := cursor
	err := huh.NewSelect[string]().
		Title(title).
		Description(description).
		Options(pickerOptions(accounts, p, def)...).
		Value(&choice).
		WithTheme(theme()).
		Run()
	return choice, err
}

// pickerOptions marks the default account with the same ● as csw status.
func pickerOptions(accounts []Account, p paths, def string) []huh.Option[string] {
	width := 0
	for _, a := range accounts {
		width = max(width, len(a.Name))
	}
	opts := make([]huh.Option[string], len(accounts))
	for i, a := range accounts {
		marker := "  "
		if a.Name == def {
			marker = nameSty.Render("●") + " "
		}
		label := fmt.Sprintf("%s%-*s  %s", marker, width, a.Name, dimSty.Render(a.Email))
		if a.Name == def {
			label += "  " + nameSty.Render("[default]")
		}
		opts[i] = huh.NewOption(label, a.Name)
	}
	return opts
}

func announce(a Account, showSwitchHint bool) {
	line := nameSty.Render("● " + a.Name)
	if a.Email != "" {
		line += "  " + dimSty.Render(a.Email)
	}
	if showSwitchHint {
		line += dimSty.Render("  · default; csw <account> for another, csw --clear to pick each time")
	}
	fmt.Fprintln(os.Stderr, line)
}

func withSpinner(title string, action func()) {
	if !isatty.IsTerminal(os.Stderr.Fd()) {
		action()
		return
	}
	_ = spinner.New().Title(" " + title).Output(os.Stderr).Action(action).Run()
}

func confirm(title, description string, def bool) (bool, error) {
	v := def
	err := huh.NewConfirm().Title(title).Description(description).Value(&v).WithTheme(theme()).Run()
	return v, err
}

func askName(title, placeholder string, taken func(string) bool) (string, error) {
	var v string
	err := huh.NewInput().
		Title(title).
		Placeholder(placeholder).
		Value(&v).
		Validate(func(s string) error {
			switch {
			case s == "":
				return fmt.Errorf("enter a name, e.g. %s", placeholder)
			case checkName(s) != nil:
				return checkName(s)
			case taken(s):
				return fmt.Errorf("%q is taken", s)
			}
			return nil
		}).
		WithTheme(theme()).
		Run()
	return v, err
}

type accountView struct {
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	Root       bool   `json:"root"`
	Email      string `json:"email,omitempty"`
	Plan       string `json:"plan,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
	LoggedIn   bool   `json:"logged_in"`
	Error      string `json:"error,omitempty"`
	Mismatch   bool   `json:"email_mismatch,omitempty"`
	Default    bool   `json:"default"`
}

func buildViews(cfg *Config, p paths, statuses []authStatus) []accountView {
	views := make([]accountView, len(cfg.Accounts))
	for i, a := range cfg.Accounts {
		st := statuses[i]
		v := accountView{
			Name: a.Name, Dir: a.Dir, Root: cfg.isRoot(a, p.home), Default: a.Name == cfg.Default,
			Email: st.Email, Plan: st.SubscriptionType, AuthMethod: st.AuthMethod, LoggedIn: st.LoggedIn,
		}
		if v.Email == "" {
			v.Email = a.Email
		}
		if st.Err != nil {
			v.Error = firstLine(st.Err.Error())
		}
		v.Mismatch = st.LoggedIn && a.Email != "" && st.Email != "" && !strings.EqualFold(a.Email, st.Email)
		views[i] = v
	}
	return views
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writePlain(w io.Writer, views []accountView) {
	for _, v := range views {
		state := "logged-out"
		if v.LoggedIn {
			state = v.AuthMethod
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Email, v.Plan, state, v.Dir)
	}
}

func renderStatus(views []accountView) string {
	rows := make([][]string, len(views))
	for i, v := range views {
		marker := " "
		if v.Default {
			marker = "●"
		}
		name := v.Name
		if v.Root {
			name += dimSty.Render(" (root)")
		}
		var state string
		switch {
		case v.Error != "":
			state = badSty.Render("error: " + v.Error)
		case !v.LoggedIn:
			state = badSty.Render("logged out")
		case v.Mismatch:
			state = warnSty.Render("email changed")
		default:
			state = goodSty.Render("logged in")
		}
		rows[i] = []string{marker, name, v.Email, v.Plan, state}
	}

	return table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(dimSty).
		Headers("", "ACCOUNT", "EMAIL", "PLAN", "STATUS").
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			s := lipgloss.NewStyle().Padding(0, 1)
			switch {
			case row == table.HeaderRow:
				return s.Foreground(subtle).Bold(true)
			case col == 0 || col == 1:
				return s.Foreground(accent).Bold(true)
			}
			return s
		}).
		Render()
}

func statusHints(views []accountView) []string {
	var hints []string
	for _, v := range views {
		switch {
		case v.Error == "" && !v.LoggedIn:
			hints = append(hints, fmt.Sprintf("log in %s: csw -a %s auth login", v.Name, v.Name))
		case v.Mismatch:
			hints = append(hints, fmt.Sprintf("%s is now logged in as a different email (a /login inside a session replaces it); log in again: csw %s auth login", v.Name, v.Name))
		}
	}
	return hints
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
