# aska

Aska is a package manager for AI agent skills. It installs skills straight from git repositories, including individual skills inside monorepos, into the directories that Codex, Claude Code, OpenCode and pi scan. It aims for the convenience of an AUR helper such as `yay` while fetching, resolving and caching like the go command: bare repositories shared across versions, immutable commits, content-addressed snapshots and a lockfile that reproduces an installation exactly.

## Quick start

```bash
just install                  # builds and installs into ~/.local/bin

# Inside a git repository: install into .agents/skills and .claude/skills
aska add github.com/owner/repo/skills/code-review   # review the files, then confirm

aska list                     # requested, resolved and installed revisions
aska update                   # review the diffs, then confirm
aska verify                   # compares installed files with the lockfile
```

Commit `.aska/skills.toml` and `.aska/skillslock.toml`; a teammate runs `aska sync` to get exactly the same skills.

## Commands

| Command | Purpose |
| --- | --- |
| `aska add <package>...` | Resolve, fetch and validate skills, review them and install them after confirmation. |
| `aska remove <skill>...` | Delete installed directories and forget the skills. |
| `aska list` | Show requested queries, resolved versions, revisions, agents and status. |
| `aska info <skill>` | Show the details of one skill, including every installed directory. |
| `aska update [skill...]` | Resolve newer compatible versions, review the diffs and install them after confirmation. |
| `aska diff <skill>` | Show the full patch an update would apply; `--to <query>` compares against another version. |
| `aska verify [skill...]` | Check installed files against the locked digests; fails when anything differs. |
| `aska sync` | Install exactly what the lockfile records without resolving any version. |

Every command works on the git repository around the working directory unless `--global` (`-g`) selects the user's own skills. Outside a repository, project commands fail and suggest `--global`. `add --agent codex,pi` chooses the agents; without it, new skills use the configured default agents (Codex and Claude Code) and reinstalled skills keep theirs. `--yes` (`-y`) on `add` and `update` installs without asking, which is required when stdin is not a terminal.

## Reviewing skills

A skill is instructions your agent will follow, and it may bundle scripts that the agent later runs, so aska shows what it is about to install before anything is written, much like yay's diff menu or paru's review. `add` and `update` first fetch, extract and validate every skill, then list the pending changes:

```text
:: 2 skill(s) to review
   1  code-review  new     v1.2.0 (64ebd1304332)                    3 file(s), 1 executable
   2  writer       update  branch main (5fde6029b82e) → branch main (2997be9b0e61)  2 files changed, 2 insertions(+), 1 deletion(-)
:: Skills to inspect? [A]ll [N]one [Ab]ort or numbers such as 1 3-4 (default: all)
==>
```

The selected skills open in a pager. A new skill shows its file listing, with executable files highlighted, followed by every file in full with line numbers and `SKILL.md` first; binary files are named but not shown and very large files are truncated. An update shows the diffstat and the full patch against the installed version. When the pager closes, aska asks `Proceed with installation? [y/N]`, which defaults to no; declining or aborting leaves the scope untouched.

Only content the scope has not installed before is offered for review: reinstalling the same content for other agents, or re-recording an edited query that selects the same commit, proceeds without a prompt. Checks that need no changes on disk, such as agent naming rules and unmanaged directories, run before the review, so nothing that is bound to fail is offered for review. If another process changes the same skill while the review is open, aska refuses to apply and asks you to run the command again, so what is installed is always exactly what was shown.

Review is on by default. `--no-review` skips the inspection menu but still asks for confirmation, `--review` forces it when the configuration turns it off, and `--yes` installs without any prompt. Text from repositories is sanitized before it reaches the pager or the terminal. The pager is chosen like git's: `ASKA_PAGER`, then `review.pager` from the configuration, then `PAGER`, then `less -R` with `LESS=FRX` unless `LESS` is set; `cat` writes straight to the terminal. `sync` installs the lockfile without a review, because its content was reviewed when it was locked.

## Package references

References follow the go command's import paths. The repository is discovered from the path, and the rest of the path is the directory that contains `SKILL.md`:

```bash
aska add github.com/owner/repo/skills/code-review          # latest stable release
aska add github.com/owner/repo/skills/code-review@v1.2.3   # exact version
aska add github.com/owner/repo/skills/code-review@^1.2     # range
aska add github.com/owner/repo/skills/code-review@main     # branch
aska add github.com/owner/repo/skills/code-review@3f2a9c1  # commit
```

On `github.com`, `bitbucket.org` and `codeberg.org` the repository is the first two path elements. On any host, a path element ending in `.git` ends the repository path, for example `gitlab.com/group/sub/repo.git/skills/x`. For other hosts, aska fetches `https://<path>?aska-get=1` and reads a tag of the form `<meta name="aska-import" content="<prefix> git <repository URL> [<subdirectory>]">`, confirming a tag found below the root against the root's own page as the go command does. Repositories can also be named explicitly, which is useful for ssh remotes: `git+ssh://git@example.com/repo.git//skills/x@v1.2.3`.

Version queries:

| Query | Selects |
| --- | --- |
| none, `latest` | The highest stable release. An `add` without a version records `^MAJOR.MINOR.PATCH` of the resolved release, as `cargo add` does, so later updates stay within the major version. |
| `v1.2.3`, `1.2.3` | Exactly that release, which may be a prerelease. |
| `v1`, `v1.2`, `^1.2`, `~1.2.3`, `>=1.2 <2` | The highest stable release in the range. Prereleases are only selected exactly. |
| a branch or tag name, `refs/heads/x`, `refs/tags/x` | That ref. Branches are followed by `update`. |
| a full or abbreviated commit id | That commit. |

A repository without version tags has no "latest" release; aska then asks for an explicit branch or commit rather than silently tracking the default branch.

### Repository-wide and per-skill tags

Release tags are either repository-wide, `v1.2.3`, or per-skill, `skills/code-review/v1.2.3`, following the go command's convention for modules in subdirectories. Only canonical semantic versions count. The scheme is chosen deterministically: a skill at the repository root uses repository-wide tags; a skill in a subdirectory uses its per-skill tags whenever it has at least one and ignores repository-wide tags; otherwise it uses repository-wide tags. Resolution fails rather than guess when two tags of the chosen scheme have equal precedence but different commits, when the selected version exists in both schemes at different commits, and when a name refers to both a branch and a tag.

## Agents

| Agent | Project directory | User directory | Frontmatter rules |
| --- | --- | --- | --- |
| `codex` | `.agents/skills` | `~/.agents/skills` | `name` and `description` required |
| `claude` | `.claude/skills` | `~/.claude/skills` (or `$CLAUDE_CONFIG_DIR/skills`) | all optional; a missing `name` falls back to the directory name |
| `opencode` | `.agents/skills` | `~/.agents/skills` | `name` and `description` required; `name` must match the directory |
| `pi` | `.agents/skills` | `~/.agents/skills` | `description` required |

A skill is installed once per directory, so Codex, OpenCode and pi share a single copy. Every selected agent must accept the skill's `SKILL.md`, otherwise the installation fails before anything is written. New agents are added by implementing the adapter interface in `internal/agent`.

## Files

```text
$XDG_CONFIG_HOME/aska/config.toml         settings, default agents, directory overrides
$XDG_DATA_HOME/aska/repos/<host>/<path>.git  bare repositories, shared by all versions and skills
$XDG_DATA_HOME/aska/packages/<package>/<sha256>  immutable, content-addressed snapshots
$XDG_STATE_HOME/aska/skills.toml          user-scope manifest
$XDG_STATE_HOME/aska/skillslock.toml      user-scope lockfile
<repository>/.aska/skills.toml            project manifest
<repository>/.aska/skillslock.toml        project lockfile
```

The manifest records what was requested:

```toml
[skills.code-review]
source = 'git+https://github.com/acme/agent-skills.git'
path = 'skills/code-review'
version = '^1.4'
agents = ['claude', 'codex']
```

The lockfile records what it resolved to. The digest is the SHA-256 of a canonical, sorted listing of every file's path, executable bit and content hash, so it identifies a skill by content alone:

```toml
lock-version = 1

[[skill]]
name = 'code-review'
source = 'git+https://github.com/acme/agent-skills.git'
path = 'skills/code-review'
requested = '^1.4'
version = 'v1.4.2'
tag = 'skills/code-review/v1.4.2'
commit = '3f2a9c1b2e4d5f60718293a4b5c6d7e8f9012345'
sha256 = 'f55178b083ed430db224eaa7ea89fd93ac4d79827aaf1efc4f7c123ee1f58539'
```

Both files are written atomically under a per-scope file lock; the lockfile changes only after every directory has been published. Bare repositories are fetched under per-repository locks, so concurrent invocations are safe.

## Security model

Every repository and skill is treated as untrusted. Nothing fetched is ever executed: repositories are bare and created without hook templates, extraction reads raw blobs through `git ls-tree` and `git cat-file --batch` so no filters, attributes or `export-subst` apply, and diffs run with external drivers and text conversion disabled.

Before any byte reaches the disk, the tree is checked for absolute paths, `..` and `.git` elements, control characters, symbolic links, submodules, names that collide on case-insensitive filesystems, and file count, file size, total size and depth limits. Files are then written through an `os.Root`, which independently refuses to escape the destination, and the content digest is computed from the bytes as written. Snapshots are verified against their digest whenever they are reused.

Installed directories are only replaced or deleted when their content matches the locked digest or a snapshot aska extracted; anything else is reported as unmanaged and left alone unless `--force` is given. Interrupted runs leave nothing half-installed: copies are staged next to the skills directory and published with a rename, staging debris is cleared under the scope lock, and `sync` recognizes content aska produced even when the lockfile was not updated.

Repository URLs must use https or ssh unless `allow_insecure` is set, and are canonicalized so that they cannot inject git options. Discovery talks HTTPS only, refuses redirects to plain HTTP, limits redirect chains and response sizes, and refuses connections to loopback, private, link-local and other non-public addresses after name resolution unless `allow_private_networks` is set. Ref names advertised by remotes and text shown on the terminal are validated or escaped so that a remote cannot inject terminal control sequences.

`add` and `update` install nothing until the user confirms, by default after inspecting the files, unless `--yes` is given. Versions are pinned to full commit ids, and `update` refuses to follow a tag that moved since it was locked; `sync` warns and installs the locked commit. A commit id proves content identity, not who published it. The installer accepts trust policies through the `install.TrustPolicy` interface, which is where signature verification or a trusted registry can be added.

## Configuration

All settings are optional; unknown keys are rejected.

```toml
default_agents = ["codex", "claude"]

[security]
allow_insecure = false          # permit http:// and git:// repositories
allow_private_networks = false  # let discovery reach private addresses

[limits]
max_files = 1000
max_file_size = 8388608         # bytes
max_total_size = 33554432       # bytes

[git]
command = "git"
timeout = "10m"

[review]
enabled = true                  # show the inspection menu before installing
pager = ""                      # shell command; ASKA_PAGER overrides, PAGER is the fallback

[agents.claude]
user_dir = "~/dotfiles/claude/skills"

[agents.codex]
project_dir = ".agents/skills"

[log]
level = "info"                  # debug, info, warn or error; --verbose forces debug
```

## Development

The project needs Go 1.27 and git. Tools are pinned in their own modules below `tools/`.

```bash
just check      # lint configuration, formatting, linters, vet, race tests, module tidiness
just test       # unit and integration tests; they use local repositories, never the network
just lint       # golangci-lint pinned in tools/golangci-lint/go.mod
just build      # bin/aska with the version from git describe
just install    # build, then install into ~/.local/bin
```

`just install` takes `--prefix` (default `~/.local`) and `--destdir` (default empty) and installs the command into `DESTDIR/PREFIX/bin`. `PREFIX` must be absolute; `DESTDIR` is prepended verbatim, following the GNU convention for staged installs, so distribution packaging can use it like this:

```bash
just build
just install --prefix=/usr --destdir="$pkgdir"   # installs $pkgdir/usr/bin/aska
```

## Known limitations

- On macOS, `github.com/adrg/xdg` places configuration, data and state below `~/Library/Application Support` unless the XDG variables are set.
- Discovery connects directly and ignores proxy environment variables, because a proxy would hide the destination address from the private network check. Git itself still honours its proxy configuration.
- The cache directory is unused: skills are read straight from git objects, so there are no archives to download.
- `sync` does not offer a review, even when it installs a skill for the first time from a lockfile someone else committed; review lockfile changes in your version control before syncing.
- The pager runs through `sh -c`, as git's does, so review paging needs a POSIX shell.
- Pseudo-versions for untagged commits, signature verification and a cache clean-up command are not implemented yet.
