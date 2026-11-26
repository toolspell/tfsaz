# Diagnose a CLI or checkout

## Run doctor

Use `tfsaz doctor [PATH]` or `tfsaz doctor --json [PATH]`. Run it in the same environment as the failing program: a terminal and an app launched from Finder may resolve different executables.

The report identifies the `az` selected by this process’s PATH, the config file, and the profile selected by the checkout. What it checks depends on where it runs:

| Context | What doctor checks |
| --- | --- |
| Managed checkout | Runs the repository verifier and checks the selected collection’s saved PAT. |
| No checkout path, working directory outside Git | Checks all configured profiles in order. |
| Unrelated Git checkout | Skips authentication. Pass the path to your managed clone for a definite checkout result. |

A failed check produces a nonzero exit, including with JSON output.

## Help and tracing

Offline help works for groups and individual commands, including `tfsaz repo setup --help` and `az devops invoke --help`.

To trace an Azure command, set `TFSAZ_TRACE=1` in its launch environment. Trace lines on stderr contain the command, profile ID, selection reason, HTTP status and elapsed time. Regular stdout is preserved. The trace omits arguments, URLs, identities, headers and request and response bodies.
