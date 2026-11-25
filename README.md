# tfsaz

`tfsaz` is a planned standalone Go CLI for using on-premises Azure DevOps Server with stock T3 Code. It will authenticate directly to configured collections with personal access tokens and implement the Azure CLI commands that T3 invokes.

The intended installation is one executable per platform. Users can opt in by exposing it as `az` on the T3 server process's PATH (a symlink on macOS or Linux, or an `az.exe` link or copy on Windows). The original CLI can remain installed.

The first milestone is a linked pull request whose state and policy checks refresh from a real collection, including when a different collection uses a different identity. This repository currently contains the project brief and agent instructions.
