package main

import "fmt"

func main() { fmt.Println(used()) }

func used() string { return "used" }

func unmarked() string { return "dead" }

// kept stays for a reason the fixture states.
//
// deadcode-allow: kept on purpose for the test.
func kept() string { return "kept" }
