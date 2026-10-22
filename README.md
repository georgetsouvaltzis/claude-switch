# csw

Switch between Claude subscriptions in Claude Code. Separate logins, shared settings, and the active account in your status line.

![Picking an account and starting Claude Code](docs/images/demo.svg)

## Install

```sh
go install github.com/georgetsouvaltzis/claude-switch/cmd/csw@latest
```

Requires [Claude Code](https://code.claude.com/docs/en/setup). If it's missing, csw offers to install it.

## Usage

Run `csw` once to set up your accounts, then:

```sh
csw                      # pick an account
csw work                 # start Claude on "work"
csw work "fix the bug"   # with a prompt; any Claude flag works too
csw work --save          # make "work" the default (csw --clear to undo)

csw add <name>           # add an account
csw rename <old> <new>   # rename an account
csw remove <name>        # log out and remove an account
csw status               # list accounts and logins

csw statusline install   # show the account and email in Claude's status line
csw statusline colors    # pick its colors
```

`csw <command> --help` shows every option.

![Account and email in Claude's status line](docs/images/statusline.svg)

## How it works

Claude Code keeps an account's login and data in a config folder. Your existing `~/.claude` becomes the main account; every account you add gets its own folder, like `~/.claude-work`, and csw starts Claude with `CLAUDE_CONFIG_DIR` pointing at it.

- **Separate per account:** login, sessions and history, and the account's `.claude.json` state, such as MCP servers added with `claude mcp add`.
- **Shared from `~/.claude`:** `settings.json`, `CLAUDE.md`, `keybindings.json`, `agents`, `commands`, `skills`, `plugins` and `output-styles`, as links. Something you create in `~/.claude` later is linked into each account the next time csw starts it. If an account already has its own copy of an item, csw leaves it alone. The `shared` list in `~/.config/csw/config.toml` controls what's linked.

Removing an account asks what to do with its data: keep the folder, move its sessions to the main account, or delete it. Deleting is permanent. csw never deletes `~/.claude`, and only deletes folders it created itself.

## License

MIT
