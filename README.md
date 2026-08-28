# git-scaffold

[![CI](https://github.com/stephenc/git-scaffold/actions/workflows/ci.yml/badge.svg)](https://github.com/stephenc/git-scaffold/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/stephenc/git-scaffold?sort=semver)](https://github.com/stephenc/git-scaffold/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/stephenc/git-scaffold.svg)](https://pkg.go.dev/github.com/stephenc/git-scaffold)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

`git-scaffold` maintains a selected set of files in a Git repository from an
upstream Git repository. It is not a one-shot project generator: the
relationship with the upstream scaffold persists, and targets can explicitly
update to later versions of their configured source.

Installed as `git-scaffold`, it runs as a Git subcommand:

```sh
git scaffold init https://github.com/acme/go-template.git
git scaffold init --existing https://github.com/acme/go-template.git
git scaffold check
git scaffold diff
git scaffold apply
git scaffold update
git scaffold outdated
git scaffold repatch
git scaffold propose
```

See [DESIGN.md](DESIGN.md) for the full specification.

## Sixty seconds

A scaffold is an ordinary Git repository with a descriptor that names the
files it manages and the arguments consumers fill in:

```toml
# template/.git-scaffold/config.toml
[scaffold]
version = 1

[[arguments]]
name = "project"
token = "@@PROJECT@@"

[[files]]
path = "Makefile"

[[files]]
path = ".github/workflows/*.yml"
```

Wherever `@@PROJECT@@` appears in `Makefile` or a workflow, the consumer's
value is substituted. A consumer points at the scaffold and supplies the
arguments:

```toml
# orders/.git-scaffold/config.toml
[scaffold]
version = 1

[source]
git = "https://github.com/acme/template.git"
ref = "main"

[args]
project = "orders"
```

`git scaffold init` writes that file for you; from then on:

```sh
git scaffold check      # do my managed files match the locked scaffold commit?
git scaffold outdated   # has the scaffold moved on since I locked it?
git scaffold update     # bring the managed files to the current scaffold commit
```

The scaffold commit in use is pinned in `.git-scaffold/lock`, so `update` is
an explicit, reviewable step — never a surprise.

## Already have 37 slightly different repositories?

That is the normal case, and it is the one `git scaffold init --existing`
is built for:

```sh
cd orders
git scaffold init --existing https://github.com/acme/template.git --arg project=orders
git scaffold check   # ✅️ clean, immediately
```

Every managed file that already differs from the scaffold is captured as an
explicit override under `.git-scaffold/patches/` and registered in
`.git-scaffold/config.toml`. Nothing you already have is lost, `check` is
clean from the first second, and every divergence is now a visible patch you
can whittle away over time — or keep, deliberately, as the documented way
this repository differs. Repeat across the fleet and `git scaffold update`
starts working for all of them.

Where the scaffold permits `json-patch` for a JSON or YAML file and both
sides parse, the difference is captured as a structured RFC 6902 patch and
the file is normalized to the canonical serialization; everything else
becomes a `text-patch` with the file left byte-for-byte untouched.
`text-patch` is always available to targets as the universal escape hatch;
structured strategies such as `json-patch` require the scaffold to permit
them per file rule.

> **YAML and automatic json-patch.** Applying a json-patch to YAML
> canonicalizes the whole document: comments are dropped, anchors expanded,
> and YAML 1.1 scalars such as `on`, `yes` or `0755` resolved. So
> `init --existing` and `repatch` generate a json-patch for a YAML file
> **only when the scaffold's own file is already canonical** — in practice,
> free of comments and anchors. A commented `.golangci.yml` falls back to
> `text-patch` even when its rule says `patch = "json-patch"`. This is
> deliberately conservative for v0.1; pass `--text-patch` to opt out of
> structured adoption entirely. If the target already declares a `json-patch`
> override for the file, `repatch` honours that explicit choice regardless of
> comments. JSON files are unaffected.

## How it works

A target repository declares, in `.git-scaffold/config.toml`, an upstream
scaffold repository, values for the arguments that scaffold defines, and
explicit local patches where the target intentionally differs. The upstream
repository's own `.git-scaffold/config.toml` declares which files it manages,
which arguments targets may or must provide, and which files may be patched.

The exact upstream commit in use is recorded in `.git-scaffold/lock`. Given
the configuration, the locked commit, the argument values, and the patches,
the contents of every managed file are deterministic. Manual edits to managed
files are not a customization mechanism — `git scaffold check` reports them as
discrepancies.

## Evolving overrides

Managed files are outputs, so hand edits show up as discrepancies in
`git scaffold check`. To keep an edit, run `git scaffold repatch`: it reads
the current content of every managed file and rewrites the overrides and
patch files to reproduce it — one patch per file, `json-patch` where
permitted (or `text-patch` with `--text-patch`), files that `check` already
accepts left alone, overrides dropped for files returned to the scaffold's
content, stale patch files deleted, and the comments in `config.toml`
preserved. `check` passes right afterwards.

## Tool configuration

Global defaults live in `$XDG_CONFIG_HOME/git-scaffold/config.toml`
(`~/.config/git-scaffold/config.toml` by default):

```toml
cache-dir = "~/.cache/git-scaffold"  # where source repos are cached

[update-check]
enabled = true
interval = "24h"
```

At most once per interval, and only when stderr is a terminal, git-scaffold
checks GitHub for a newer release and prints a one-line notice to stderr.
Set `enabled = false`, or the `GIT_SCAFFOLD_NO_UPDATE_CHECK` or `CI`
environment variables, to disable the check entirely.

## Design notes

### Rule precedence is deliberately blunt

When file rules in a scaffold descriptor overlap, exactly one form of
precedence exists: an **exact path** overrides a **glob** that also matches
it. There is no "more specific glob beats less specific glob" — two globs (or
two exact rules) matching the same file must imply identical behavior, or the
descriptor is rejected as ambiguous.

This is by design, not a missing feature. Specificity ordering between
patterns invites descriptors whose behavior a reader has to compute rather
than read. If one file in a globbed directory needs different treatment, name
it — the carve-out is then visible at a glance. The nudge is intentional:
scaffold only what you need, with the smallest, most explicit rule set that
says it.

## License

[Apache License 2.0](LICENSE)
