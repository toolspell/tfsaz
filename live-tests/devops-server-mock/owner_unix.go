//go:build !windows

package main

import (
	"log"
	"os"
	"syscall"
)

// chownLike gives path the owner and group of like.
func chownLike(path string, like os.FileInfo) {
	if st, ok := like.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(path, int(st.Uid), int(st.Gid)); err != nil {
			log.Printf("chown %s: %v", path, err)
		}
	}
}
