package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

type Account struct {
	Name  string `toml:"name"`
	Dir   string `toml:"dir"`
	Email string `toml:"email,omitempty"`
	// Colors override [statusline_colors] for this account; "" inherits.
	Colors StatuslineColors `toml:"colors,omitempty"`
}

type Config struct {
	Root           string `toml:"root"`
	StatuslineWrap string `toml:"statusline_wrap,omitempty"`
	Default        string `toml:"default,omitempty"`
	// StatuslineColors goes last: TOML tables must follow plain keys.
	StatuslineColors StatuslineColors `toml:"statusline_colors"`

	// colorsOutdated marks a [statusline_colors] section that lacks a key or
	// has one csw no longer uses; loadApp rewrites it to the current keys.
	colorsOutdated bool
	Shared         []string  `toml:"shared"`
	Accounts       []Account `toml:"accounts"`
}

var defaultShared = []string{
	"settings.json",
	"CLAUDE.md",
	"keybindings.json",
	"agents",
	"commands",
	"skills",
	"plugins",
	"output-styles",
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// checkName rejects names that `csw <name>` couldn't reach because a csw or
// claude command already owns that word.
func checkName(name string) error {
	switch {
	case !validName.MatchString(name):
		return fmt.Errorf("%q: use letters, digits, - and _", name)
	case slices.Contains(subcommandNames(), name), slices.Contains(claudeSubcommands, name):
		return fmt.Errorf("%q is a command name; pick another", name)
	}
	return nil
}

var errNoConfig = errors.New("no config yet")

type paths struct {
	home   string
	config string
	last   string
}

func resolvePaths() (paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return paths{}, err
	}
	cfgDir := filepath.Join(home, ".config", "csw")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		cfgDir = filepath.Join(xdg, "csw")
	}
	return paths{
		home:   home,
		config: filepath.Join(cfgDir, "config.toml"),
		last:   filepath.Join(cfgDir, "last"),
	}, nil
}

func defaultClaudeDir(home string) string {
	return filepath.Join(home, ".claude")
}

// detectRoot mirrors where Claude Code itself looks when csw is not involved.
func detectRoot(home string) string {
	if dir := os.Getenv(configDirEnv); dir != "" {
		return dir
	}
	return defaultClaudeDir(home)
}

// expandHome also takes %USERPROFILE%, which Windows users type and see in
// csw's messages.
func expandHome(path, home string) string {
	const profile = "%USERPROFILE%"
	if len(path) >= len(profile) && strings.EqualFold(path[:len(profile)], profile) {
		path = "~" + path[len(profile):]
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return filepath.Clean(path)
}

// shownPath renders a config path for messages. The config keeps "~/" so it
// works on every OS; Windows users read %USERPROFILE%\ instead, the way
// Claude Code's docs write it.
func shownPath(p string) string { return shownPathFor(p, isWindows) }

func shownPathFor(p string, windows bool) string {
	if !windows {
		return p
	}
	p = strings.ReplaceAll(p, "/", `\`)
	if rest, ok := strings.CutPrefix(p, `~\`); ok {
		return `%USERPROFILE%\` + rest
	}
	return p
}

func abbreviateHome(path, home string) string {
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

func newConfig(root, home string) *Config {
	return &Config{
		Root:   abbreviateHome(filepath.Clean(expandHome(root, home)), home),
		Shared: append([]string(nil), defaultShared...),
	}
}

func loadConfig(path string) (*Config, error) {
	var cfg Config
	meta, err := toml.DecodeFile(path, &cfg)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoConfig
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	for _, f := range colorFields {
		cfg.colorsOutdated = cfg.colorsOutdated || !meta.IsDefined("statusline_colors", f.key)
	}
	for _, k := range meta.Undecoded() {
		inColors := len(k) > 1 && k[0] == "statusline_colors" || len(k) > 2 && k[0] == "accounts" && k[1] == "colors"
		cfg.colorsOutdated = cfg.colorsOutdated || inColors
	}
	if cfg.Root == "" {
		// v1 configs had no root key and always meant ~/.claude.
		cfg.Root = "~/.claude"
		if err := saveConfig(path, &cfg); err != nil {
			return nil, err
		}
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	out := *cfg
	out.StatuslineColors = cfg.StatuslineColors.withDefaults()
	if err := toml.NewEncoder(tmp).Encode(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (c *Config) rootDir(home string) string {
	return expandHome(c.Root, home)
}

func (c *Config) dir(a Account, home string) string {
	return expandHome(a.Dir, home)
}

func (c *Config) isRoot(a Account, home string) bool {
	return samePath(c.dir(a, home), c.rootDir(home))
}

func (c *Config) find(name string) (Account, bool) {
	for _, a := range c.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return Account{}, false
}

func (c *Config) lookup(name string) (Account, error) {
	if a, ok := c.find(name); ok {
		return a, nil
	}
	msg := fmt.Sprintf("no account %q", name)
	if s := suggest(name, c.names()); s != "" {
		return Account{}, fmt.Errorf("%s, did you mean %q?", msg, s)
	}
	return Account{}, fmt.Errorf("%s (have: %s)", msg, strings.Join(c.names(), ", "))
}

func (c *Config) names() []string {
	names := make([]string, len(c.Accounts))
	for i, a := range c.Accounts {
		names[i] = a.Name
	}
	return names
}

func (c *Config) remove(name string) {
	c.Accounts = slices.DeleteFunc(c.Accounts, func(a Account) bool { return a.Name == name })
	if c.Default == name {
		c.Default = ""
	}
}

func (c *Config) rename(from, to string) {
	for i := range c.Accounts {
		if c.Accounts[i].Name == from {
			c.Accounts[i].Name = to
		}
	}
	if c.Default == from {
		c.Default = to
	}
}

func (c *Config) setEmail(name, email string) {
	for i := range c.Accounts {
		if c.Accounts[i].Name == name {
			c.Accounts[i].Email = email
		}
	}
}

func loadLastUsed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveLastUsed(path, name string) error {
	return os.WriteFile(path, []byte(name+"\n"), 0o644)
}

func suggest(input string, candidates []string) string {
	best, bestDist := "", 3
	for _, c := range candidates {
		if d := levenshtein(strings.ToLower(input), strings.ToLower(c)); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// typoOf returns the candidate one keystroke away from input (a swapped pair
// counts as one), or "". Strict on purpose: anything further is more likely
// a one-word prompt than a typo.
func typoOf(input string, candidates []string) string {
	in := strings.ToLower(input)
	for _, c := range candidates {
		if c != input && osaDistance(in, strings.ToLower(c)) <= 1 {
			return c
		}
	}
	return ""
}

// osaDistance is Levenshtein plus adjacent transpositions.
func osaDistance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
