package main

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// StatuslineColors are always written out in full, defaults included, so
// the config file shows every key a user can change. An empty value means
// the default. Each value is "#RRGGBB", "#RGB" or a 0-255 color number.
type StatuslineColors struct {
	Name  string `toml:"name"`
	Email string `toml:"email"`
}

type colorField struct {
	key, label string
	def        string
	get        func(*StatuslineColors) *string
}

var colorFields = []colorField{
	{"name", "account name", "173", func(c *StatuslineColors) *string { return &c.Name }},
	{"email", "email", "245", func(c *StatuslineColors) *string { return &c.Email }},
}

var hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// sgr turns a color spec into an ANSI foreground parameter.
func sgr(spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if hexColor.MatchString(spec) {
		h := spec[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		v, _ := strconv.ParseUint(h, 16, 32)
		return fmt.Sprintf("38;2;%d;%d;%d", v>>16, v>>8&0xff, v&0xff), nil
	}
	if n, err := strconv.Atoi(spec); err == nil && n >= 0 && n <= 255 {
		return fmt.Sprintf("38;5;%d", n), nil
	}
	return "", fmt.Errorf("%q: use #RRGGBB, #RGB or a number 0-255", spec)
}

// palette maps a colorField key to its ANSI parameter ("" = terminal text).
type palette map[string]string

// resolve never fails: the status line must always draw, so an invalid
// value falls back to the default.
func (c StatuslineColors) resolve() palette {
	p := palette{}
	for _, f := range colorFields {
		code, err := sgr(*f.get(&c))
		if err != nil {
			code, _ = sgr(f.def)
		}
		p[f.key] = code
	}
	return p
}

// over layers c on top of base: c's valid values win, the rest come from
// base. An account's colors go over the global ones this way.
func (c StatuslineColors) over(base StatuslineColors) StatuslineColors {
	for _, f := range colorFields {
		if v := *f.get(&c); v != "" {
			if _, err := sgr(v); err == nil {
				*f.get(&base) = v
			}
		}
	}
	return base
}

func (cfg *Config) colorsFor(acct Account) palette {
	return acct.Colors.over(cfg.StatuslineColors).resolve()
}

func (c StatuslineColors) withDefaults() StatuslineColors {
	for _, f := range colorFields {
		if v := f.get(&c); *v == "" {
			*v = f.def
		}
	}
	return c
}

func (c StatuslineColors) invalid() []string {
	var bad []string
	for _, f := range colorFields {
		if v := *f.get(&c); v != "" {
			if _, err := sgr(v); err != nil {
				bad = append(bad, f.key+" = "+err.Error())
			}
		}
	}
	return bad
}

func paint(code, s string) string {
	if code == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// swatches is the picker's palette; any other color goes through "Custom".
var swatches = []struct{ name, hex string }{
	{"coral", "#DA7756"}, {"red", "#E57373"}, {"orange", "#FFB74D"}, {"yellow", "#FFF176"},
	{"lime", "#C5E1A5"}, {"green", "#81C784"}, {"teal", "#4DB6AC"}, {"cyan", "#4DD0E1"},
	{"blue", "#64B5F6"}, {"indigo", "#7986CB"}, {"purple", "#BA68C8"}, {"pink", "#F06292"},
	{"white", "#FFFFFF"}, {"silver", "#BDBDBD"}, {"gray", "#8A8A8A"}, {"slate", "#5C5C66"},
	{"charcoal", "#3E3E46"}, {"black", "#1E1E24"},
}

const (
	pickSave   = "\x00save"
	pickReset  = "\x00reset"
	pickCustom = "\x00custom"
)

func swatch(spec string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(strings.TrimSpace(spec))).Render("████")
}

// runEditor shows one step of the color editor full screen, so steps don't
// pile up in the scrollback, with esc as a way out.
func runEditor(fields ...huh.Field) error {
	keys := huh.NewDefaultKeyMap()
	keys.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "back"))
	return huh.NewForm(huh.NewGroup(fields...)).
		WithTheme(theme()).
		WithKeyMap(keys).
		WithProgramOptions(tea.WithAltScreen()).
		Run()
}

// colorScope is what the editor changes: the global colors, or one
// account's overrides on top of them.
type colorScope struct {
	label   string
	account *Account // nil for all accounts
	target  *StatuslineColors
}

func (a *app) scopeFor(name string) (colorScope, error) {
	if name == "" {
		return colorScope{label: "all accounts", target: &a.cfg.StatuslineColors}, nil
	}
	for i := range a.cfg.Accounts {
		if acct := &a.cfg.Accounts[i]; acct.Name == name {
			return colorScope{label: acct.Name, account: acct, target: &acct.Colors}, nil
		}
	}
	_, err := a.cfg.lookup(name)
	return colorScope{}, err
}

// inherited is what an empty value means in this scope.
func (a *app) inherited(sc colorScope, f colorField) (spec, from string) {
	if sc.account == nil {
		return f.def, "default"
	}
	return cmp.Or(*f.get(&a.cfg.StatuslineColors), f.def), "all accounts"
}

// preview draws the status line for every account the edit affects, with
// edited in place of the scope's current colors.
func (a *app) preview(sc colorScope, edited StatuslineColors) string {
	global := a.cfg.StatuslineColors
	if sc.account == nil {
		global = edited
	}
	var lines []string
	for _, acct := range a.cfg.Accounts {
		own := acct.Colors
		switch {
		case sc.account != nil && acct.Name == sc.account.Name:
			own = edited
		case sc.account != nil:
			continue
		}
		lines = append(lines, renderSegment(acct.Name, cmp.Or(acct.Email, "me@example.com"), own.over(global).resolve()))
	}
	return strings.Join(lines, "\n")
}

func (a *app) chooseScope() (string, error) {
	opts := []huh.Option[string]{huh.NewOption("All accounts", "")}
	for _, acct := range a.cfg.Accounts {
		label := acct.Name
		if acct.Colors != (StatuslineColors{}) {
			label += dimSty.Render("  (own colors)")
		}
		opts = append(opts, huh.NewOption(label, acct.Name))
	}
	var name string
	err := runEditor(huh.NewSelect[string]().
		Title("Status line colors for").
		Description(a.preview(colorScope{target: &a.cfg.StatuslineColors}, a.cfg.StatuslineColors) + "\n" + dimSty.Render("an account's own colors override the ones for all accounts")).
		Options(opts...).Value(&name))
	return name, err
}

func (a *app) editColors(account string, reset bool) error {
	if account == "" && !reset && interactive() && len(a.cfg.Accounts) > 1 {
		var err error
		if account, err = a.chooseScope(); errors.Is(err, huh.ErrUserAborted) {
			return nil
		} else if err != nil {
			return err
		}
	}
	sc, err := a.scopeFor(account)
	if err != nil {
		return err
	}
	if reset {
		*sc.target = StatuslineColors{}
		if err := a.save(); err != nil {
			return err
		}
		done("status line colors for %s reset", sc.label)
		return nil
	}
	if !interactive() {
		return fmt.Errorf("not a terminal; set colors in %s ([statusline_colors], or [accounts.colors] per account)", a.p.config)
	}

	edited := *sc.target
	width := 0
	for _, f := range colorFields {
		width = max(width, len(f.label))
	}
	for {
		opts := make([]huh.Option[string], 0, len(colorFields)+2)
		for _, f := range colorFields {
			v := *f.get(&edited)
			spec, from := a.inherited(sc, f)
			shown := v
			if v == "" || (sc.account == nil && v == f.def) {
				shown = cmp.Or(spec, "terminal text") + dimSty.Render(" ("+from+")")
			}
			opts = append(opts, huh.NewOption(fmt.Sprintf("%-*s  %s %s", width, f.label, swatch(cmp.Or(v, spec)), shown), f.key))
		}
		resetLabel := "Reset all to defaults"
		if sc.account != nil {
			resetLabel = "Use the colors for all accounts"
		}
		opts = append(opts, huh.NewOption("Save", pickSave), huh.NewOption(resetLabel, pickReset))

		choice := pickSave
		err := runEditor(huh.NewSelect[string]().
			Title("Status line colors for " + sc.label + ": pick one to change").
			Description(a.preview(sc, edited) + "\n" + dimSty.Render("esc closes without saving")).
			Options(opts...).
			Value(&choice))
		if errors.Is(err, huh.ErrUserAborted) {
			if edited != *sc.target {
				hint("closed without saving")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch choice {
		case pickSave:
			*sc.target = edited
			if err := a.save(); err != nil {
				return err
			}
			done("status line colors for %s saved; Claude picks them up on its next refresh", sc.label)
			return nil
		case pickReset:
			edited = StatuslineColors{}
			continue
		}
		for _, f := range colorFields {
			if f.key == choice {
				if err := a.pickColor(sc, f, &edited); err != nil && !errors.Is(err, huh.ErrUserAborted) {
					return err
				}
			}
		}
	}
}

// pickColor shows swatches for one field. Moving the cursor updates the
// preview, so the user sees each color in place before choosing it.
func (a *app) pickColor(sc colorScope, f colorField, edited *StatuslineColors) error {
	target := f.get(edited)
	choice := *target
	spec, from := a.inherited(sc, f)
	opts := []huh.Option[string]{huh.NewOption(swatch(spec)+" "+from+" "+dimSty.Render(cmp.Or(spec, "terminal text")), "")}
	for _, s := range swatches {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s %-8s %s", swatch(s.hex), s.name, dimSty.Render(s.hex)), s.hex))
	}
	opts = append(opts, huh.NewOption("Custom: type #RRGGBB or 0-255…", pickCustom))
	if choice != "" && !slices.ContainsFunc(opts, func(o huh.Option[string]) bool { return o.Value == choice }) {
		opts = slices.Insert(opts, 1, huh.NewOption(swatch(choice)+" current "+dimSty.Render(choice), choice))
	}

	preview := func() string {
		trial := *edited
		if choice != pickCustom {
			*f.get(&trial) = choice
		}
		return a.preview(sc, trial)
	}
	err := runEditor(
		huh.NewSelect[string]().Title(f.label+" · "+sc.label).Description(dimSty.Render("enter picks · esc goes back")).
			Options(opts...).Value(&choice).Height(12),
		huh.NewNote().Title("Preview").DescriptionFunc(preview, &choice),
	)
	if err != nil {
		return err
	}
	if choice == pickCustom {
		custom := *target
		err := runEditor(huh.NewInput().Title(f.label + " · " + sc.label).Description(dimSty.Render("esc goes back")).
			Placeholder("#DA7756 or 173").Value(&custom).
			Validate(func(s string) error { _, err := sgr(s); return err }))
		if err != nil {
			return err
		}
		choice = strings.TrimSpace(custom)
	}
	*target = choice
	return nil
}
