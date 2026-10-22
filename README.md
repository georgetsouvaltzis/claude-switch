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

Each account is its own Claude Code config folder (`~/.claude-work`), so each keeps its own login, sessions and history. Settings, `CLAUDE.md`, skills, plugins and the like are linked from your main folder (`~/.claude`), so they stay the same everywhere.

csw never deletes your main folder, and never deletes anything permanently: removed accounts go to the trash.

## License

MIT
