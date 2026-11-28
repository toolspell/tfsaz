package main

import "os"

// chownLike does nothing: Windows has no Unix owner to copy, and a native run
// there writes the keys as the user who runs the mock.
func chownLike(string, os.FileInfo) {}
