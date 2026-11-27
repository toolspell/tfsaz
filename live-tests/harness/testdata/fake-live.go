package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
)

func main() {
	const clonePrefix = "tfsaz-live-bedtest-ubuntu-"
	const clone = clonePrefix + "test"
	args := os.Args[1:]
	switch {
	case slices.Equal(args, []string{"services", "status", "-json"}):
		dir, err := os.Getwd()
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"instance": "bedtest", "running": []string{"router", "dns", "devops-server-mock"}, "stateHome": dir,
				"ports": map[string]int{"http": 8083, "https": 8446, "ssh": 2225, "journal": 19093, "wireguard": 51823},
			})
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case len(args) == 5 && slices.Equal(args[:4], []string{"vm", "up", "ubuntu", "-name"}):
		fmt.Printf("{\"name\":%q,\"os\":\"ubuntu\",\"bedAddress\":\"172.30.56.129\",\"ignored\":\"extra field\"}\n", clonePrefix+args[4])
	case len(args) == 3 && slices.Equal(args[:2], []string{"vm", "down"}) && strings.HasPrefix(args[2], clonePrefix):
	case slices.Equal(args, []string{"mock", "requests", "-vm", clone, "-json"}):
		fmt.Println(`[{"time":"2026-11-27T10:00:00Z","clientIP":"172.30.56.129","method":"GET","path":"/","status":200}]`)
	case slices.Equal(args, []string{"mock", "reset"}):
	case slices.Equal(args, []string{"bad-json"}):
		fmt.Println("unexpected text")
	case slices.Equal(args, []string{"failed", "--", "guest script"}):
		fmt.Fprintln(os.Stderr, "tool error")
		os.Exit(3)
	case len(args) >= 3 && slices.Equal(args[:3], []string{"vm", "run", clone}):
		script := "printf '%s\\n' 'hello world'; exit 7"
		if slices.Equal(args[3:], []string{"-json", "-timeout", "2m0s", "--", script}) {
			fmt.Println(`{"exitCode":7,"stdout":"hello world\n","stderr":"guest warning\n","timedOut":false,"seconds":0.25}`)
			return
		}
		if slices.Equal(args[3:], []string{"-json", "-timeout", "2m0s", "-gui", "--", script}) {
			fmt.Println(`{"exitCode":124,"stdout":"","stderr":"","timedOut":true,"seconds":120}`)
			return
		}
		badArgs(args)
	case slices.Equal(args, []string{"vm", "browser", "start", clone, "chrome", "-port", "9333", "about:blank"}):
		fmt.Println("http://127.0.0.1:9333")
	case len(args) == 4 && slices.Equal(args[:3], []string{"vm", "forward", clone}):
		port, guestPort, _ := strings.Cut(args[3], ":")
		if guestPort != "9333" {
			badArgs(args)
		}
		if runtime.GOOS == "windows" {
			listen(port)
			return
		}
		child := exec.Command(os.Args[0], "listen", port)
		child.Stderr = os.Stderr
		if err := child.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case len(args) == 2 && args[0] == "listen":
		listen(args[1])
	default:
		badArgs(args)
	}
}

func listen(port string) {
	listener, err := net.Listen("tcp4", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() { _ = listener.Close() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

func badArgs(args []string) {
	fmt.Fprintln(os.Stderr, "unexpected arguments", args)
	os.Exit(2)
}
