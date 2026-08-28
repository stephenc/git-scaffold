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
git scaffold check
git scaffold diff
git scaffold apply
git scaffold update
git scaffold outdated
git scaffold propose
```

See [DESIGN.md](DESIGN.md) for the full specification.

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

## Adopting an existing repository

`git scaffold init --existing` brings a repository that already has content
under scaffold management, guaranteed: any managed file that differs from the
scaffold's materialization is captured as an explicit `text-patch` override
under `.git-scaffold/patches/`, so `git scaffold check` is clean immediately
and every divergence is visible as a patch you can whittle away over time.
`text-patch` is always available to targets as the universal escape hatch;
structured strategies such as `json-patch` require the scaffold to permit
them per file rule.

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
