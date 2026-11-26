// Command dev runs tfsaz's local developer checks and worktree setup.
//
//	go run ./tools/dev check [-base main]  fast check of this branch's changes, before a commit
//	go run ./tools/dev ready               full local suite, before a push
//	go run ./tools/dev staged              staged-only checks, run by .githooks/pre-commit
//	go run ./tools/dev setup [-tools]      prepare a fresh worktree
//
// The tools are pinned in one module each under tools/<name>/go.mod; see
// docs/operations/development.md.
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `usage: go run ./tools/dev <command>

  check [-base main]  fast check of this branch's changes, before a commit
  ready               full local suite, before a push
  staged              staged-only checks (the pre-commit hook)
  setup [-tools]      enable the hooks and download modules; -tools also builds the tools
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		os.Exit(1)
	}
	if err := os.Chdir(root); err != nil {
		fmt.Fprintln(os.Stderr, "dev:", err)
		os.Exit(1)
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	var ok bool
	switch os.Args[1] {
	case "check":
		base := flags.String("base", "main", "compare against the merge base with this ref")
		_ = flags.Parse(os.Args[2:])
		ok = check(*base)
	case "ready":
		_ = flags.Parse(os.Args[2:])
		ok = ready()
	case "staged":
		_ = flags.Parse(os.Args[2:])
		ok = staged()
	case "setup":
		tools := flags.Bool("tools", false, "also build the pinned tools")
		_ = flags.Parse(os.Args[2:])
		ok = setup(*tools)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}
