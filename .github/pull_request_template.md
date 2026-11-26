<!--
Use a conventional commit title, such as "fix(cli): doctor reports a missing git".
Solve one problem per PR. Never include a PAT, real hostname or collection URL.
-->

## Problem

<!-- The problem in a sentence or two. Close its issue with "Closes #<n>", or use "Refs #<n>"
while work remains. -->

## Change

<!-- How this fixes the problem. Note any change to a command T3 calls or to credential routing. -->

## Verification

<!-- The checks you ran and what you observed. State anything you could not check.
"Tests pass" alone is not evidence. -->

- [ ] `gofmt -l .` prints nothing, `go vet ./...` and `go test -count=1 ./...` pass
- [ ] `TestStockT3Adapter` ran, or this PR touches no command T3 calls

### Platforms

<!-- TFSaz needs feature parity across macOS, Windows and Linux for a release. For each
platform, tick it if you tested on it, or leave it unticked and say why (for example "CI only"
or "no platform-specific code"). A platform this PR doesn't implement yet needs "not supported
on this platform" handling and a platform:<os> issue; link it here. -->

- [ ] macOS:
- [ ] Windows:
- [ ] Linux:

<!-- If you used an agent, end with the model and harness that did the work. -->
