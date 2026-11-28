# Live VM smoke results, November 2026

`TestSmoke` from the [Go live-test harness](../operations/live-tests.md#write-a-live-test) ran against the [live-test mock](../operations/live-tests.md) on an Apple Silicon host, one guest at a time, from the branch of PR #114.

| Guest | Result | Time |
| --- | --- | --- |
| Ubuntu | passed | 48.46 s |
| macOS | passed | 47.52 s (package time) |
| Windows | passed | 129.45 s |

Each run covered a guest shell command, a command in the desktop session, an HTTP request to the mock and its journal entry, a PNG screenshot, the T3 Code installation and T3's debugging port through a host forward. Windows also passed in 127.59 s on an earlier retry, after the shared base lock cleared.

The macOS run passed after `vm t3 start` switched to launching the installed app by its bundle path. The Windows runs passed after the fixes for the shared base lock, `vm forward` and the app-picker policy.

Not covered: the full test suite in each guest's desktop session ([#48](https://github.com/toolspell/tfsaz/issues/48)) and TFSaz itself in the guests.
