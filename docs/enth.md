# Using pogo with an enth repo

pogo manages two kinds of repository. A **git-backed** project is the default:
workers get a git worktree and a branch, and the refinery merges their work. An
**enth-backed** project keeps its history in [enth](https://enth.app), a
version-control system served by an `enthd` server. pogo handles it
differently from end to end:

| | git-backed | enth-backed |
|---|---|---|
| Worker's workspace | a git worktree on a branch | an enth checkout (`.enth/`, no `.git`, no branch) |
| How work is named | its branch | `Pogo-Item` / `Pogo-Worker` trailers on its Changes |
| How work lands | the refinery merge queue runs the gates and merges | the worker runs `enth land` itself; `pogo land` records it |
| Tested before landing | the merged result, by the refinery | the worker's own base only; CI runs on the GitHub mirror after the land |
| Pull-request track | yes (`polecat-build-pr`) | no: refused at spawn |
| Stranded work | pushed, unmerged branches | unlanded Changes |

The design and its reasons are in [design/enth-vcs.md](design/enth-vcs.md).
This page covers setup.

## 1. Install and sign in to enth

1. **Install the `enth` CLI** so `enth` is on the PATH of the user pogod runs
   as. pogo needs enth #9 or later, the version with `enth status DIR` (check
   with `enth --help`). An older enth writes checkouts pogo cannot read.
2. **Sign in.** Pick one:
   - `enth login https://enth.app` (or your own enthd URL), run as the user
     pogod runs as. Every enth project on that remote uses this login.
   - Give one project its own token, such as a bot user's, with
     `pogo project set-enth-token <path> --file=<token file>`. pogo stores the
     file's path, never the token, and gives the token only to that project's
     workers.
   - `ENTH_TOKEN` in pogod's environment. pogod passes it to its workers.
3. **An author identity** for the workers' `enth change`. pogod supplies one
   from git's `user.name` / `user.email` when enth would find none, so a host
   with a `~/.gitconfig` needs nothing more. Otherwise set `ENTH_AUTHOR_NAME`
   and `ENTH_AUTHOR_EMAIL`.

## 2. Register the project as enth

pogo needs three facts a directory scan cannot supply: the enthd URL, the repo
name, and the trunk ref. You give them on the project record.

```sh
enth --remote https://enth.app --repo owner/name checkout main ~/dev/name
pogo project add ~/dev/name --vcs=enth --enth-remote=https://enth.app --enth-repo=owner/name
```

The directory is a read checkout that pogo keeps fresh. It is yours, like a
git project's clone; workers never edit it. `--enth-trunk` defaults to
`refs/heads/main`.

**A project registered automatically is recorded as git.** `pogo visit`, the
shell hooks and `[search] index_roots` register any directory holding `.git`
or `.enth/checkout`, and they record it with no `vcs` key. A record with no key
is git, so a dispatch onto it tries to make a git worktree and fails. Fix the
record with:

```sh
pogo project set-vcs ~/dev/name --vcs=enth --enth-remote=https://enth.app --enth-repo=owner/name
```

`pogo doctor --check` finds these records (next section) and prints that
command with the values filled in from the checkout.

## 3. Check it works

```sh
pogo project list --json   # the record shows "vcs": "enth" and an "enth" block
pogo doctor --check        # the enth rows below
```

`pogo doctor --check` prints its enth rows only when they apply, so a host with
only git repos prints none of them:

- **`enth repo recorded as git`** (warning): an enth checkout whose record says
  git. Run the `set-vcs` line it prints.
- **`enth in PATH`**: fails when no `enth` is found, and warns when the `enth`
  found is older than #9. The PATH checked is your shell's, which can differ
  from pogod's (a launchd service has its own).
- **`enth login`**: one row per remote (and per token file). It runs
  `enth whoami` with the token the workers will use, and passes with the user
  that token names. It fails when enth refuses the token or the token file
  cannot be read. It warns when enthd cannot be reached.

The end-to-end check is one small work item: file it against the repo
(`mg new --repo=<path> "…"`), let the coordinator dispatch it, and watch for the
item to close. It closes with a `landed` record naming the enth land, which you
can see with `mg show <id>`.

`scripts/enth_new_repo_scenario_test.sh` rehearses this whole page against a
sandboxed pogod and enthd: a fresh install, `enth login`, the coordinator's
new-repo recipe lifted from the shipped prompt, a worker landing the first
Change, and the auto-registration trap. It needs `enth` and `enthd` on PATH
(or `POGO_ENTH_BIN_DIR`), and skips without them.

## 4. What changes for the people and agents running it

- **Workers land their own work.** The worker template's `self_land` step runs
  `pogo presubmit`, then `enth land`, then `pogo land`. `pogo land` checks the
  land against enthd and closes the item, just as a refinery merge does.
  `pogo refinery submit` on an enth repo is refused.
- **Nothing tests the merged result before it lands.** The worker tests its own
  base, and `enth land` merges onto the current trunk. CI on the GitHub mirror
  after the land is the safety net.
- **No pull-request track.** Spawning `polecat-build-pr`, or a `--branch` other
  than the trunk, onto an enth repo is refused with a 409. Dispatch the
  ordinary worker template.
- **Recovery is by Change, not by branch.** `pogo check-stranded` and the
  `[stranded-push]` mail list an item's unlanded Changes and print the
  `enth land … && pogo land --item` remedy. There is no branch to push, submit
  or delete, and no git worktree to prune. Run `git` commands against an enth
  workspace and they fail; use `enth status <dir>` to see a workspace's
  uncommitted edits. A committed Change is already on enthd, so removing a
  workspace can lose only its uncommitted edits.
