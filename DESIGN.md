# `git-scaffold` Requirements Specification

**Status:** Initial implementation specification\
**Target:** v0.1\
**Binary:** `git-scaffold`\
**Git invocation:** `git scaffold`

## 1. Purpose

`git-scaffold` maintains a selected set of files in a Git repository
from an upstream Git repository.

It is not a one-shot project generator.

A target repository declares:

-   an upstream Git repository;
-   an optional upstream branch/tag/ref;
-   values for arguments declared by the upstream scaffold;
-   explicit local patches where the target intentionally differs from
    the upstream scaffold.

The upstream repository declares:

-   which files or file globs it manages;
-   which arguments targets may/must provide;
-   how those arguments are represented as literal tokens globally and,
    optionally, per file/file rule;
-   which managed files permit local patching and by which strategy.

The exact upstream commit used to materialize the target is recorded in
a lock file.

The central invariant is:

> Given the target configuration, locked upstream commit, upstream
> descriptor, target argument values and target patches, the expected
> contents of all scaffold-managed files are deterministic.

Manual modifications to generated managed files are not a supported
customization mechanism.

## 2. Design Principles

### 2.1 Declarative

Configuration is data, not executable code. TOML is used for scaffold
configuration.

### 2.2 Git-native

Sources are Git repositories and source versions are Git commits.

The executable is named `git-scaffold`, allowing:

``` sh
git scaffold ...
```

### 2.3 Maintained, not generated once

The relationship with the source repository persists after initial
creation. Targets can explicitly update to later versions of their
configured source.

### 2.4 Source owns the scaffold interface

The source determines managed files, argument definitions, token
representations, and allowed patch mechanisms. Targets provide values
and intentional overrides.

### 2.5 Local divergence is explicit

Persistent differences from upstream must be represented by argument
values, local patches, or native extension mechanisms provided by the
managed format.

The current contents of a generated file are not interpreted as an
implicit local override.

### 2.6 Deterministic

Ordinary materialization uses the locked source commit. A moving
branch/tag/ref MUST NOT silently change generated output.

### 2.7 Transactional

A failed update MUST NOT leave a partially updated repository or
advanced lock.

### 2.8 Single repository

`git-scaffold` operates on one Git worktree. Fleet management is outside
its scope.

External tools such as `gits` may invoke it across a fleet:

``` sh
gits --repo-host github.com --repo-contains example-org \
    git scaffold update
```

## 3. Target Repository Layout

A target repository contains:

``` text
.git-scaffold/
    config.toml
    lock
    patches/
        ...
```

`patches/` is optional. Before initial materialization, `lock` may be
absent. All scaffold metadata SHOULD be committed to Git.

## 4. Target Configuration

Example:

``` toml
[scaffold]
version = 1

[source]
git = "https://github.com/acme/go-template.git"
ref = "main"

[args]
project_name = "orders"
module = "github.com/acme/orders"

[overrides.".golangci.yml"]
strategy = "json-patch"
patches = [
    "patches/golangci.json"
]
```

## 5. Configuration Version

Every configuration MUST contain:

``` toml
[scaffold]
version = 1
```

Unknown versions MUST be rejected.

The same configuration format is used in source and target repositories.
A repository MAY be both a scaffold source and a scaffold target.

## 6. Source Declaration

A target MUST declare:

``` toml
[source]
git = "<git-url>"
```

It MAY declare:

``` toml
ref = "<ref>"
```

Any Git URL syntax supported by the installed Git implementation MAY be
used.

`git-scaffold` MUST NOT attempt to distinguish branches from tags or
other refs. It asks Git to resolve the configured source and optional
ref to a commit.

If `ref` is absent, the source repository's default/HEAD ref is used.

## 7. Lock File

The lock file is:

``` text
.git-scaffold/lock
```

Its complete contents are:

``` text
<full Git commit SHA>\n
```

Example:

``` text
71db8b7497d60831eae98e2d9b70548fdb39f714
```

It MUST NOT contain other metadata, including source URL/ref, timestamp,
file list, hashes, or tool version.

The configuration describes the desired source lineage. The lock
identifies the exact source commit currently materialized.

## 8. Source Descriptor

A source repository declares its scaffold in:

``` text
.git-scaffold/config.toml
```

Example:

``` toml
[scaffold]
version = 1

[template]
name = "go-service"

[[arguments]]
name = "project_name"
description = "Project name"
token = "@@PROJECT_NAME@@"

[[arguments]]
name = "module"
description = "Go module path"

[[arguments]]
name = "go_version"
description = "Go toolchain version"
default = "1.26"
token = "@@GO_VERSION@@"

[[files]]
path = "Makefile"

[[files]]
path = "go.mod"

[files.arguments.module]
token = "@@MODULE@@"

[[files]]
path = ".github/workflows/*.yml"

[[files]]
path = ".golangci.yml"
patch = "json-patch"

[[files]]
path = "examples/**/*"
substitute = false
allow-empty = true
```

Only files selected by `[[files]]` entries are managed. Other files in
the source repository are irrelevant to subsequent synchronization.

This allows the source repository to simultaneously be an ordinary
GitHub template repository containing additional one-time scaffolding
content.

## 9. Managed Files and Glob Patterns

A `[[files]]` entry MAY identify either a single file or a glob pattern
matching multiple files.

Examples:

``` toml
[[files]]
path = "Makefile"

[[files]]
path = ".github/workflows/*.yml"

[[files]]
path = ".github/ISSUE_TEMPLATE/**"

[[files]]
path = "config/**/*.yaml"
```

Patterns are evaluated against files in the source Git tree at the
selected source commit. Only regular files matched by a rule are
materialized. Directories themselves are not managed objects.

### 9.1 Glob Syntax

V0.1 MUST support conventional path-oriented glob syntax:

``` text
*       zero or more non-separator characters
?       exactly one non-separator character
**      zero or more complete path components
```

Matching MUST use `/` as the logical path separator regardless of host
operating system and MUST operate against repository-relative paths.

Patterns MUST NOT escape the repository root.

### 9.2 Exact Paths

A path containing no glob metacharacters is an exact managed path. The
referenced file MUST exist in the source commit. Failure to find an
exact path is an error.

### 9.3 Empty Glob Matches

A glob matching zero files is an error by default.

A source MAY explicitly permit an empty match:

``` toml
[[files]]
path = ".github/optional/**/*.yml"
allow-empty = true
```

`allow-empty` defaults to `false`.

For an exact non-glob path, `allow-empty` MUST NOT suppress a
missing-file error.

### 9.4 Deterministic Expansion

Glob expansion MUST be deterministic. Matched paths MUST be normalized
to repository-relative `/`-separated paths and sorted lexicographically
before further processing.

Filesystem enumeration order MUST NOT affect materialization.

### 9.5 Overlapping Rules

An exact (non-glob) `[[files]]` entry is more specific than a glob
entry. If a file is matched by both an exact entry and one or more glob
entries, the exact entry alone governs that file. This allows a glob to
manage a directory while an exact rule carves out different behavior
for one file within it.

Otherwise — multiple glob entries, or multiple exact entries, matching
the same source file — the overlap is permitted only if their effective
configuration is identical.

If such overlapping entries imply different substitution or patch
behavior, the descriptor MUST be rejected as ambiguous.

Declaration order MUST NOT resolve conflicting rules.

### 9.6 Source and Target Paths

The matched source path is preserved as the target path.

Path remapping, destination prefixes, filename substitution, and
flattening are out of scope for v0.1.

## 10. Arguments

The source MAY define arguments.

Each argument supports:

``` toml
[[arguments]]
name = "project_name"
description = "Project name"
default = "example"
token = "@@PROJECT_NAME@@"
```

Properties:

-   `name`: required stable public name used by targets.
-   `description`: optional human-readable description.
-   `default`: optional; if absent, the argument is required.
-   `token`: optional default literal token to replace in managed files.

An argument MAY omit a global token and define tokens only for specific
files/file rules.

## 11. Target Argument Values

Targets provide argument values by name:

``` toml
[args]
project_name = "orders"
module = "github.com/acme/orders"
```

Resolution is:

``` text
target value
    ↓ if absent
source default
    ↓ if absent
ERROR
```

A missing required argument MUST cause materialization to fail.

The argument name is the stable source/target contract. Targets do not
need to know token syntax.

## 12. Literal Token Substitution

`git-scaffold` has no template language. It performs literal token
replacement.

Given:

``` toml
[[arguments]]
name = "project_name"
token = "@@PROJECT_NAME@@"
```

and target:

``` toml
[args]
project_name = "orders"
```

then `github.com/acme/@@PROJECT_NAME@@` becomes
`github.com/acme/orders`.

The source author MAY choose any token string, for example:

``` text
@@PROJECT_NAME@@
{{ project.name }}
__PROJECT_NAME__
%PROJECT%
some-unique-placeholder
```

`git-scaffold` MUST NOT interpret token syntax. Tokens are literal
strings, not regular expressions, expressions, variables, glob patterns,
or template syntax.

## 13. Per-File / Per-Rule Argument Tokens

A managed file rule MAY override the token used for an argument.

Example:

``` toml
[[arguments]]
name = "project_name"
token = "@@PROJECT_NAME@@"

[[files]]
path = "docs/**/*.md"

[files.arguments.project_name]
token = "{{ project.name }}"
```

Every concrete file matched by that rule uses `{{ project.name }}` as
its effective `project_name` token.

For files governed by other rules, the global token remains
`@@PROJECT_NAME@@`.

The target remains unaware of token representation.

## 14. Arguments Without Global Tokens

This is valid:

``` toml
[[arguments]]
name = "module"
description = "Module identifier"

[[files]]
path = "go.mod"

[files.arguments.module]
token = "@@MODULE@@"

[[files]]
path = "build.yml"

[files.arguments.module]
token = "{{ module }}"
```

An argument does not require a global token.

## 15. Per-File Argument Disable

A file rule MAY disable a specific argument:

``` toml
[[files]]
path = "documentation/examples/**/*.txt"

[files.arguments.project_name]
enabled = false
```

`enabled` defaults to `true`.

If disabled, occurrences of the global token in concrete files matched
by that rule remain untouched.

## 16. Disable Substitution Entirely

A managed file rule MAY declare:

``` toml
[[files]]
path = "fixtures/**/*"
substitute = false
```

This disables all argument substitution for every file matched by the
rule.

`substitute` defaults to `true`.

Combining `substitute = false` with per-file/per-rule argument token
configuration MUST be rejected.

## 17. Effective Token Resolution

For each concrete `(file, argument)` pair:

``` text
file substitution disabled?
        │
       yes → no substitution
        │
        no
        ↓
argument disabled for matching rule?
        │
       yes → no substitution
        │
        no
        ↓
rule-specific token?
        │
       yes → use it
        │
        no
        ↓
global argument token?
        │
       yes → use it
        │
        no
        ↓
no substitution
```

## 18. Simultaneous Replacement

All argument replacements in a file MUST conceptually operate
simultaneously against the original source content.

Given:

``` text
A token = @@A@@
A value = @@B@@

B token = @@B@@
B value = hello
```

then `@@A@@ @@B@@` MUST produce `@@B@@ hello`, not `hello hello`.

Replacement order MUST NOT affect output.

## 19. Token Validation

For any individual concrete managed file, two enabled arguments MUST NOT
resolve to the same effective token.

An explicitly configured token MUST NOT be empty.

Different files MAY use the same literal token for different arguments
if there is no ambiguity within any single file.

Declared argument tokens MAY occur zero, one, or multiple times. Unused
tokens are not errors.

## 20. Replacement Scope

Substitution applies only to managed file contents.

V0.1 does NOT substitute arguments into file paths, directory names,
source URLs, refs, patch paths, config files, or descriptor paths.

Path substitution is out of scope.

## 21. Byte Preservation

Ordinary unaffected portions of managed files SHOULD be preserved
byte-for-byte.

Literal token substitution MUST NOT unnecessarily normalize line
endings, Unicode, whitespace, or encoding.

For simple substitution, implementations SHOULD operate on byte strings
where practical.

## 22. Native Extension Mechanisms

Native extension mechanisms are preferred over patches.

For example, a source `Makefile` may contain:

``` make
-include Makefile.local
```

The source owns `Makefile`, while the target owns `Makefile.local`.

Likewise reusable CI workflows and native configuration includes SHOULD
be preferred where practical.

## 23. Patchable Files

A source MAY permit a managed file or all files matched by a rule to be
locally patched:

``` toml
[[files]]
path = ".golangci.yml"
patch = "json-patch"
```

or:

``` toml
[[files]]
path = "config/**/*.yml"
patch = "json-patch"
```

`text-patch` is ALWAYS available to targets as the universal escape
hatch: a target MAY declare `text-patch` overrides against any managed
file, whether or not its rule declares `patch`.

A rule's `patch = "<strategy>"` declaration additionally permits that
structured strategy (currently `json-patch`) for the matched files.

A target MUST NOT choose a structured patch strategy not permitted by
the source.

A source file/rule without `patch` does not permit *structured* target
patching; `text-patch` remains available.

## 24. Target Overrides

A target declares patches against concrete repository-relative paths:

``` toml
[overrides.".golangci.yml"]
strategy = "json-patch"
patches = [
    "patches/golangci.json"
]
```

For a glob-managed file:

``` toml
[overrides."config/production/service.yml"]
strategy = "json-patch"
patches = [
    "patches/production-service.json"
]
```

Patch paths are relative to `.git-scaffold/`.

Multiple patches are applied in declaration order.

Target override keys MUST be concrete paths. Target-side glob overrides
are out of scope for v0.1.

## 25. Materialization Pipeline

Materialization first expands the source descriptor:

``` text
source descriptor
       +
source Git tree
       ↓
expand exact paths + globs
       ↓
validate overlaps
       ↓
concrete managed-file set
```

Then, for each concrete managed file:

``` text
source file
     ↓
resolve argument values
     ↓
determine effective tokens
     ↓
simultaneous literal substitution
     ↓
apply target patches in order
     ↓
materialized target file
```

Patches therefore see post-substitution content.

## 26. JSON Patch

V0.1 MUST support RFC 6902 JSON Patch against JSON and YAML files.

For YAML:

``` text
parse YAML
    ↓
JSON-compatible object model
    ↓
apply RFC 6902 operations
    ↓
serialize YAML
```

A failed patch operation MUST fail materialization.

The implementation MUST NOT silently repair, reinterpret, fuzzily apply,
or ignore a failed patch.

If an explicit downstream patch is incompatible with a new upstream
version, human resolution is required.

## 27. Text Patch

V0.1 MUST support a `text-patch` escape hatch using conventional
unified-diff semantics.

Per §23, `text-patch` is always permitted regardless of a rule's
`patch` declaration.

Failure to apply a text patch MUST fail materialization.

Fuzzy conflict resolution is not required.

## 28. Future Patch Strategies

Additional strategies MAY later include `json-merge-patch` or
format-specific structural patching.

V0.1 MUST NOT invent an ad-hoc YAML merge algorithm.

## 29. Manual Modification of Managed Files

Managed files are outputs.

If the working-tree version differs from expected materialization,
`git scaffold check` MUST report the discrepancy.

`git-scaffold` MUST NOT perform a three-way merge to preserve manual
modifications.

Persistent differences belong in `.git-scaffold/config.toml`,
`.git-scaffold/patches/`, or native local extension files.

## 30. Managed File Addition and Removal

The concrete managed set is obtained by expanding the descriptor against
a particular source commit.

During update:

``` text
descriptor @ old locked SHA → old concrete managed set
descriptor @ new SHA        → new concrete managed set
```

Therefore:

``` text
new managed path      → create
existing managed path → update if necessary
removed managed path  → delete
```

This applies whether membership changes because source files were
added/removed or because descriptor glob rules changed.

Renames may be represented as deletion plus creation. Explicit rename
metadata is not required.

No separate local managed-file manifest is required because old and new
concrete sets can be reconstructed from their source commits.

## 31. Transactionality

All modifying operations MUST construct and validate the complete
proposed result before changing target state.

Conceptually:

``` text
resolve source
      ↓
load descriptor
      ↓
expand managed rules
      ↓
load source files
      ↓
resolve arguments
      ↓
perform substitutions
      ↓
apply patches
      ↓
validate entire result
      ↓
calculate complete changes
      ↓
        success?
        /     \
      yes      no
       ↓        ↓
 write files   change
 + deletions   nothing
 + lock
```

Failures MUST leave all managed files unchanged, removed files
undeleted, new files uncreated, and the lock unchanged.

## 32. Commands

V0.1 exposes:

``` text
git scaffold init
git scaffold check
git scaffold diff
git scaffold apply
git scaffold update
git scaffold outdated
git scaffold propose
```

## 33. `git scaffold init`

Example:

``` sh
git scaffold init https://github.com/acme/go-template.git
```

or:

``` sh
git scaffold init https://github.com/acme/go-template.git --ref main
```

It:

1.  verifies the current directory belongs to a Git worktree;
2.  creates `.git-scaffold/config.toml`;
3.  resolves the source;
4.  reads and validates the source descriptor;
5.  determines required arguments;
6.  expands the managed file set;
7.  materializes the scaffold;
8.  writes `.git-scaffold/lock`.

If required arguments have not been supplied, initialization MUST fail
clearly.

Interactive prompting is not required for v0.1.

### `--existing`

`init --existing` adopts a scaffold into an existing repository whose
files already differ from the scaffold.

When a managed file already exists in the worktree and its content
differs from the materialization, init MUST record the difference as a
`text-patch` override — a unified diff of materialized → existing
content — stored under `.git-scaffold/patches/` and registered in
`[overrides]`. The pre-existing file itself MUST NOT be modified.

Files whose content already matches the materialization exactly need no
patch. Managed files absent from the worktree are materialized as with
plain init.

After `init --existing`, `check` MUST pass without any pre-existing
file having been modified.

Limit: for binary-looking files (content containing NUL bytes) a
unified text diff is not meaningful. Differing binary files MUST cause
a refusal that reports them; `--force` is required to overwrite them.
With `--existing --force`, `--force` resolves only such binary
refusals — text-adoptable files are still adopted, not overwritten.

`--existing` with no pre-existing differing files behaves like plain
init.

## 34. `git scaffold check`

Checks whether current managed files equal expected materialization
from:

``` text
target config
+ lock
+ source descriptor @ lock
+ expanded concrete managed set
+ resolved args
+ target patches
```

It MUST NOT resolve the moving source ref merely to check for updates.

It SHOULD avoid network access if the locked commit is available
locally.

Suggested exit status:

``` text
0 = correct
1 = differences/configuration/materialization problem
```

## 35. `git scaffold diff`

Calculates expected materialization using the locked commit and displays
differences against the working tree.

It MUST NOT modify the repository.

Output SHOULD use ordinary unified diff where practical.

## 36. `git scaffold apply`

Materializes the currently locked source.

It MUST NOT advance an existing lock because the configured ref has
moved.

If no lock exists, `apply` MAY resolve the configured source and create
the initial lock.

Thus: `apply` means make the working tree match what is currently
locked.

## 37. `git scaffold update`

Resolves the configured source/ref again.

If it resolves to the currently locked SHA, no source update is
required.

Otherwise it:

1.  resolves the new SHA;
2.  loads the new descriptor;
3.  expands its exact paths and globs;
4.  reconstructs the old concrete managed set from the old
    descriptor/source commit;
5.  constructs the complete new materialization;
6.  applies target arguments;
7.  applies target patches;
8.  validates the complete result;
9.  determines additions/modifications/deletions;
10. writes resulting files;
11. updates the lock.

The operation MUST be transactional.

Example:

``` text
Updating scaffold

source: https://github.com/acme/go-template.git
ref:    main

71db8b7 → 93ca782

M .golangci.yml
M Makefile
A .github/workflows/security.yml
D .github/workflows/old-ci.yml
```

On patch failure:

``` text
Updating scaffold

71db8b7 → 93ca782

error: .golangci.yml
patches/golangci.json: operation 4 failed:
path /run/timeout does not exist

No files changed.
```

## 38. `git scaffold outdated`

Resolves the configured source/ref without modifying target state.

It compares the lock SHA with the currently resolved SHA.

Suggested exit status:

``` text
0 = current
1 = update available
>1 = error
```

## 39. `git scaffold propose`

`propose` automates proposing an upstream scaffold update through Git:

``` text
update
   ↓
create/update proposal branch
   ↓
commit
   ↓
push
   ↓
create PR/MR if necessary
```

It operates only on the current repository and performs no
organization/fleet discovery.

## 40. Proposal Branch

Default:

``` text
git-scaffold/update
```

This branch is tool-owned.

Repeated `propose` executions SHOULD update the same branch. If the
scaffold advances while an update proposal remains open, rerunning
`propose` SHOULD refresh that proposal rather than create another.

Safe force-update semantics such as `--force-with-lease` SHOULD be used.

## 41. Proposal Commit

Default commit message:

``` text
chore: update repository scaffold
```

The implementation MAY include metadata trailers such as:

``` text
Git-Scaffold-Source: 93ca7823f27caab6a12699db69c7cb93723128fd
```

## 42. Proposal Description

A generated proposal SHOULD include source repository, configured ref,
old SHA, new SHA, changed managed files, and applied target patches.

## 43. Hosting Provider Integration

Hosting-provider APIs are not part of the core architecture.

Provider operations SHOULD use existing provider CLIs.

Initially:

``` text
github.com → gh
```

GitHub PR lookup/creation SHOULD invoke the `gh` CLI rather than
implementing GitHub REST or GraphQL APIs.

Future integrations MAY include GitLab via `glab`.

Unknown providers MUST NOT prevent branch creation/push. The tool should
report that the branch was pushed but automatic PR creation is
unavailable.

## 44. Custom Proposal Command

Targets MAY configure an external PR/MR creation command.

It MUST be represented as an argument array, not a shell string.

Example:

``` toml
[propose]
create-command = [
    "forge",
    "pr",
    "create",
    "--head", "{{ branch }}",
    "--title", "{{ title }}",
    "--body-file", "{{ body_file }}"
]
```

The proposal-command placeholder mechanism is independent of scaffold
argument substitution.

At minimum it SHOULD expose `branch`, `title`, and `body_file`.

The command MUST be executed directly without an implicit shell.

## 45. Proposal Idempotency

Repeated `git scaffold propose` MUST NOT create multiple open proposals
for the standard proposal branch.

If a proposal exists, update its branch.

If no update exists, create no commit and no proposal.

## 46. Fleet Operation

Fleet operation is explicitly outside `git-scaffold`.

For example:

``` sh
gits --repo-host github.com --repo-contains example-org \
    git scaffold outdated
```

or:

``` sh
gits --repo-host github.com --repo-contains example-org \
    git scaffold propose
```

`git-scaffold` MUST NOT implement organization repository enumeration,
fleet cloning, fleet concurrency, fleet filtering, or organization-wide
scheduling.

## 47. Template Inheritance

A source repository MAY itself be a scaffold target:

``` text
base-template
      ↓
go-template
      ↓
go-service-template
      ↓
service-a / service-b / service-c
```

No special recursive inheritance mechanism is required.

A template repository simply contains already-materialized files
inherited from its own source while exposing its own descriptor to
consumers.

`git-scaffold` processes only the immediate source of the current
repository. It MUST NOT recursively resolve scaffold relationships.

## 48. Security

Scaffold configuration is non-executable.

V0.1 MUST NOT provide arbitrary shell hooks, arbitrary scripting,
template expressions, embedded programming languages, or pre/post
materialization commands.

The custom proposal command is an explicit target-controlled integration
and is the sole exception.

All managed paths MUST remain within the Git worktree.

Absolute managed patterns and repository-escaping paths such as
`../../outside` MUST be rejected.

Glob patterns MUST be evaluated against the source Git tree, not
arbitrary filesystem paths.

Patch paths MUST remain beneath `.git-scaffold/`.

Symlink traversal MUST NOT permit reads or writes outside allowed
repository boundaries.

Remote scaffold contents MUST be treated as untrusted data.

## 49. Git State

The implementation MUST locate the actual Git worktree rather than
assume the current working directory is its root.

Commands MAY be executed from subdirectories.

Modifying commands SHOULD refuse operations that would overwrite
unrelated uncommitted changes.

At minimum, a file that `git-scaffold` intends to modify/delete and that
has unrelated working-tree modifications MUST cause a safe failure
unless an explicit override exists.

The implementation MUST NOT silently destroy local work.

## 50. Source Retrieval

The core design MUST NOT depend on GitHub. Source retrieval uses Git.

Implementations MAY maintain a cache of fetched scaffold
repositories/objects.

The cache is an implementation detail and MUST NOT affect deterministic
output.

A locked commit that is locally available SHOULD permit `check`, `diff`,
and `apply` without network access.

`update` and `outdated` normally require source resolution and therefore
MAY require network access.

## 51. Non-Goals

V0.1 explicitly does not include:

-   fleet management;
-   repository discovery;
-   web services;
-   arbitrary executable scaffold configuration;
-   TypeScript/Python configuration;
-   a template language;
-   conditionals;
-   loops;
-   expressions;
-   path templating;
-   automatic preservation of generated-file edits;
-   three-way merging;
-   fuzzy conflict resolution;
-   multiple upstream sources;
-   recursive scaffold resolution;
-   target-side glob overrides;
-   glob-based patch assignment by targets;
-   destination path remapping;
-   filename templating;
-   directory flattening;
-   arbitrary include/exclude rule languages;
-   interactive UI;
-   hosting-provider API clients.

Source-side glob selection IS part of v0.1.

## 52. Core Implementation Boundary

The materialization engine SHOULD be expressible approximately as:

``` text
materialize(
    source_tree,
    source_descriptor,
    target_config,
    target_patches
) -> expected_managed_tree
```

Descriptor expansion is part of materialization.

The materialization core SHOULD have no dependency on GitHub, remote
repository enumeration, PR creation, or working-tree mutation.

Conceptually:

``` text
source tree ─┐
descriptor ──┼─→ expand paths/globs
target cfg ──┤       ↓
patches ─────┘   resolve args
                  ↓
              substitute
                  ↓
                patch
                  ↓
             expected tree
                  ↓
          Git/filesystem layer
                  ↓
       check/diff/apply/update
                  ↓
          proposal integration
```

This separation SHOULD be reflected in implementation and tests.

## 53. Required Validation and Tests

At minimum, tests MUST cover:

### Repository/configuration

-   invocation outside a Git repository;
-   missing target configuration;
-   invalid TOML;
-   unsupported scaffold version;
-   invalid source;
-   unresolvable source ref;
-   missing upstream descriptor;
-   malformed upstream descriptor.

### Managed files/globs

-   exact path matching;
-   root-level `*`;
-   single-component `?`;
-   recursive `**`;
-   `/` path semantics on all supported operating systems;
-   glob matching zero files;
-   `allow-empty = true`;
-   deterministic lexical ordering;
-   overlapping identical rules;
-   overlapping conflicting rules;
-   exact rule taking precedence over an overlapping glob rule;
-   missing exact managed source file;
-   source update adding a file matching an existing glob;
-   source update removing a file matching an existing glob;
-   source descriptor widening a glob;
-   source descriptor narrowing a glob;
-   source descriptor removing a glob;
-   absolute glob rejection;
-   glob attempting repository escape;
-   matched symlink safety.

### Arguments/substitution

-   duplicate argument names;
-   duplicate effective tokens within a concrete file;
-   empty explicit token;
-   file rule referring to undefined argument;
-   missing required target argument;
-   argument default resolution;
-   target value overriding default;
-   global token substitution;
-   per-file/per-rule token substitution;
-   per-file argument disable;
-   complete rule substitution disable;
-   simultaneous/non-recursive substitution;
-   token occurring zero times;
-   token occurring multiple times;
-   argument override applied through a glob;
-   `substitute = false` applied through a glob.

### Patches

-   unauthorized structured target patch;
-   `text-patch` override accepted on a rule without `patch`;
-   patch permission inherited from a glob;
-   concrete target override of a glob-managed file;
-   rejection of glob target overrides;
-   malformed JSON Patch;
-   JSON Patch operation failure;
-   JSON Patch against JSON;
-   JSON Patch against YAML;
-   malformed structural input;
-   successful text patch;
-   failed text patch;
-   token substitution occurring before patching;
-   patch path escaping `.git-scaffold`.

### Lifecycle

-   manual managed-file modification detected by `check`;
-   source adding a managed file;
-   source removing a managed file;
-   source modifying a managed file;
-   unchanged source ref;
-   advanced source ref;
-   failed update preserving all files;
-   failed update preserving lock;
-   successful update writing lock only after successful
    materialization;
-   `apply` not advancing an existing lock;
-   `outdated` not modifying state;
-   `init --existing` adopting differing files as text patches
    (worktree untouched, `check` clean immediately after, binary
    refusal without `--force`).

### Proposals/safety

-   `propose` with no update;
-   `propose` with an existing proposal;
-   proposal branch refresh;
-   unknown hosting provider;
-   protection of unrelated local working-tree modifications.

## 54. Acceptance Scenario

Source commit A contains:

``` text
Makefile
go.mod
.golangci.yml
.github/workflows/ci.yml
.github/workflows/test.yml
```

with descriptor:

``` toml
[scaffold]
version = 1

[template]
name = "go-service"

[[arguments]]
name = "project_name"
description = "Project name"
token = "@@PROJECT_NAME@@"

[[arguments]]
name = "module"

[[arguments]]
name = "go_version"
default = "1.26"
token = "@@GO_VERSION@@"

[[files]]
path = "Makefile"

[[files]]
path = "go.mod"

[files.arguments.module]
token = "@@MODULE@@"

[[files]]
path = ".golangci.yml"
patch = "json-patch"

[[files]]
path = ".github/workflows/*.yml"
```

Target:

``` toml
[scaffold]
version = 1

[source]
git = "https://github.com/acme/go-template.git"
ref = "main"

[args]
project_name = "orders"
module = "github.com/acme/orders"

[overrides.".golangci.yml"]
strategy = "json-patch"
patches = [
    "patches/golangci.json"
]
```

Lock:

``` text
aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
```

`git scaffold check` MUST reconstruct the expected concrete managed set
and contents from commit A and verify them against the target.

Now upstream advances to commit B where:

-   `Makefile` changes;
-   `go.mod` changes but continues using the declared tokens;
-   `.golangci.yml` changes compatibly with the target patch;
-   `.github/workflows/test.yml` is removed;
-   `.github/workflows/security.yml` is added and therefore
    automatically matches the existing workflow glob.

Then:

``` sh
git scaffold update
```

MUST atomically produce the equivalent of:

``` text
M Makefile
M go.mod
M .golangci.yml
D .github/workflows/test.yml
A .github/workflows/security.yml
M .git-scaffold/lock
```

and the lock MUST contain commit B.

If the `.golangci.yml` patch cannot be applied to commit B, the
operation MUST fail and no managed file changes, deletions, additions,
or lock change may occur.

## 55. Definition of v0.1

v0.1 is complete when a user can:

``` sh
git scaffold init <git-url> --ref <optional-ref>
```

configure required arguments and optional local patches, and thereafter
reliably use:

``` sh
git scaffold check
git scaffold diff
git scaffold apply
git scaffold update
git scaffold outdated
```

to maintain the repository against the upstream scaffold, including
source-side glob expansion.

`git scaffold propose` SHOULD also be delivered in v0.1 if it does not
compromise the correctness or simplicity of the materialization/update
core.

Correct deterministic synchronization is higher priority than proposal
automation.

## 56. Tool Configuration and Update Check

This section covers tool infrastructure, not scaffold behavior. Nothing
here may affect deterministic output (§17, §50).

### Global tool configuration

An optional per-user file `$XDG_CONFIG_HOME/git-scaffold/config.toml`
(else `~/.config/git-scaffold/config.toml`; plain XDG on every platform,
matching git's own XDG handling) configures the tool. An absent file
MUST leave the tool fully functional with defaults.

Keys (all optional):

-   `cache-dir` — the source cache location (§50). A leading `~/` MUST
    expand to the user's home directory. Precedence:
    `$GIT_SCAFFOLD_CACHE` env, else `cache-dir`, else
    `$XDG_CACHE_HOME/git-scaffold`, else `~/.cache/git-scaffold`.
-   `[update-check]` `enabled` — boolean, default `true`.
-   `[update-check]` `interval` — Go duration string, default `"24h"`.
    MUST be positive.

Unknown keys MUST be rejected with a clear error (typo protection), as
with the target configuration.

### Update check

At most once per `interval`, a command MAY check the latest release by
requesting `https://github.com/stephenc/git-scaffold/releases/latest`
with redirects disabled and reading the release tag from the `Location`
header. No hosting-provider API client (§51). Request timeout MUST be
at most 2 seconds; any HTTP failure MUST be a silent no-op.

When the latest release is a newer semver than the running version, the
tool MUST print exactly one notice line, to stderr only, after command
output. The `version` command included.

The check MUST NOT run when:

-   `update-check.enabled = false`;
-   `$GIT_SCAFFOLD_NO_UPDATE_CHECK` is non-empty;
-   `$CI` is non-empty;
-   stderr is not a terminal;
-   the running version is not a plain semver (dev builds never
    prompt).

Last-check state (mtime = last check, content = last-seen tag) lives in
`$XDG_STATE_HOME/git-scaffold/update-check` (else
`~/.local/state/git-scaffold/update-check`). Unreadable or unwritable
state MUST be a silent no-op. The state MUST record every attempt, not
only successes: a failed check bumps the mtime (preserving the
last-seen-tag content) so a persistently failing network does not
re-attempt on every command within the interval.

The check MUST NOT delay a command by more than 2 seconds in total (the
request timeout MUST fit within that bound), MUST NOT change any exit
status, and MUST NOT write to stdout.
