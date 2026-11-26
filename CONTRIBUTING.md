# Contributing

<!-- Adapted from Toolspell's CONTRIBUTING.md (MIT), itself adapted from T3 Code's. -->

TFSaz has very little review capacity. Reviewing a PR usually takes more effort than writing one,
so these rules keep review possible. The most useful contributions are bug reports with a good
repro and focused fixes for bugs you hit yourself.

**Not open yet:** the repository is private for now, so only maintainers can open issues and pull
requests. This guide applies once that changes. Security reports follow
[SECURITY.md](SECURITY.md).

## Before you write code

- **Bugs** go in [issues](https://github.com/toolspell/tfsaz/issues/new/choose), one problem per
  issue. The bug template says what to include and what to remove first: never paste a PAT, and
  replace hostnames and collection URLs with placeholders.
- **Ideas and features** go in issues too, as a feature request. Wait for a maintainer to agree
  before you open a PR.
- **Security vulnerabilities** never go in public issues or PRs. Follow
  [SECURITY.md](SECURITY.md#report-a-vulnerability).

The cheapest help is closing things: link the original of a duplicate, say when a bug no longer
happens on `main`, and link what superseded a dead PR. A test report on an existing PR beats a
second PR for the same bug.

An issue labeled `needs-more-info` waits on the reporter. Without a reply in 7 days, a maintainer
decides whether to close it.

## Rules for a PR

These rules apply to every PR from someone without write access, drafts included. A PR that follows
them can still be closed. We do not owe a review or a merge.

<a id="scope"></a>

### Fix a bug, or build what was agreed

A PR fixes a bug, or does what a maintainer agreed to in an issue; link that issue. A crash, a
regression, a broken T3 Code feature and a real performance problem are bugs. A change to how
TFSaz behaves is a feature, even when the PR calls it a fix. A small fix for an obvious bug needs
no issue.

Not accepted without that agreement: new features, new commands, architecture changes, new
dependencies, and refactors or cleanups on their own. TFSaz works from outside, against the real
installed T3 Code app or server, so a change that needs T3 Code, Microsoft's Azure CLI or the
`azure-devops` extension to change is out of scope ([AGENTS.md](AGENTS.md#outside-in-only)).

<a id="one-problem"></a>

### One problem per PR

Fix one problem. Include every change that fix needs across code, tests and docs. Cleanup,
refactors and a second fix go in their own PRs.

macOS, Windows and Linux need feature parity for a release. A PR may implement a feature on fewer
platforms only with explicit "not supported on this platform" handling on the others and a
[backlog](docs/backlog.md#platforms) entry for each missing platform
([AGENTS.md](AGENTS.md#platform-parity)).

<a id="verification"></a>

### Show that it works

Say how you found the bug (or what your change does), how you checked it, and what you saw. "Tests
pass" is not evidence. A logic or protocol bug comes with a test that fails before the fix. For a
change to a command T3 Code calls, say whether `TestStockT3Adapter` ran
([Development](docs/operations/development.md)). Name the platforms you tested on in the PR
template.

Paste terminal output as text, with PATs, hostnames and collection URLs removed. Upload screenshots
to the PR; do not commit them.

If a coding agent did the work, say which model and harness at the end of the PR. The rules in
[AGENTS.md](AGENTS.md) apply to its changes too.

<a id="open-pr-limit"></a>

### 3 open PRs per person

You can have 3 open PRs. Get one merged or closed before you open a fourth.

<a id="conflict-window"></a>

### Keep your PR mergeable

A PR that conflicts with its base branch and stays that way for 7 days with no activity is closed.

<a id="bans"></a>

## Bans

- If we decide that you spam us with automation, you will be banned.
- If you make reviewing and maintaining harder, you will be banned. Examples are repeated pings, a
  fight over a closure, and a second account to get around a limit.

Any issue or PR a banned account opens is closed when it is opened.

<a id="closure-and-reconsideration"></a>

## When your PR is closed

A PR that breaks a rule gets a notice that names the rule and says how to fix it. It closes 72
hours later. A reply or a push in that time sends it back for another look.

After it is closed, fix the problem and comment on the PR, or open a focused replacement PR that
links the old one.

## Setup

1. Follow [Development](docs/operations/development.md#setup), then run
   `go run ./tools/dev setup -tools` once per checkout. It enables the pre-commit hook and builds
   the pinned tools.
2. Before you commit, run `go run ./tools/dev check`. Before you push, run
   `go run ./tools/dev ready`. Maintainers run the full suites before merging.
3. Never run commands against your real configuration, keyring or system while you develop.
   [Isolate manual runs](docs/operations/development.md#isolate-manual-runs) says how.
4. Use a conventional commit title in plain language, such as
   `fix(setup): restore no longer leaves a lock file`, and fill in the PR template. PRs are squash
   merged, so the title becomes the commit on `main`.

The [architecture](docs/internals/architecture.md) explains how TFSaz works,
[T3 compatibility](docs/internals/t3-compatibility.md) why it exists, and
[testing](docs/internals/testing.md) what a good test covers.

## License

By submitting a contribution you agree it is licensed under the MIT License (see
[LICENSE](LICENSE)). There is no CLA and no sign-off.
