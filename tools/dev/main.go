// Command dev runs TFSaz's local developer checks and worktree setup.
//
//	go run ./tools/dev check [-base main]  fast check of this branch's changes, before a commit
//	go run ./tools/dev ready               full local suite, before a push
//	go run ./tools/dev staged              staged-only checks, run by .githooks/pre-commit
//	go run ./tools/dev setup [-tools]      prepare a fresh worktree
//	go run ./tools/dev setup -undo         turn the hooks off again
//	go run ./tools/dev ci-local            run the CI workflow with act in Docker
//
// ci-changes, ci-checks and ci-test are the CI workflow's steps.
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
  setup -undo         turn the hooks off again (unset core.hooksPath)
  ci-local [-base main] [-context <docker context>] [-- <act args>]
                      run .github/workflows/test.yml with act in Docker

CI workflow steps:
  ci-changes -base <rev>  print tests=true|false: whether the native test matrix must run
  ci-checks               whole-tree gates and reports
  ci-test [-shard i/n]    the race tests, or the i-th of n parts of them
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
		undo := flags.Bool("undo", false, "unset the core.hooksPath that setup sets")
		_ = flags.Parse(os.Args[2:])
		switch {
		case *undo && *tools:
			fmt.Fprintln(os.Stderr, "setup: -undo and -tools cannot be combined")
			os.Exit(2)
		case *undo:
			ok = undoSetup()
		default:
			ok = setup(*tools)
		}
	case "ci-changes":
		base := flags.String("base", "", "the commit before the change; empty runs the tests")
		_ = flags.Parse(os.Args[2:])
		ok = ciChanges(*base)
	case "ci-checks":
		_ = flags.Parse(os.Args[2:])
		ok = ciChecks()
	case "ci-test":
		shard := flags.String("shard", "1/1", "run the i-th of n parts of the tests")
		_ = flags.Parse(os.Args[2:])
		ok = ciTest(*shard)
	case "ci-local":
		base := flags.String("base", "main", "simulate a push of the changes since the merge base with this ref")
		context := flags.String("context", "", "docker context to run the containers in")
		_ = flags.Parse(os.Args[2:])
		ok = ciLocal(*base, *context, flags.Args())
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}
