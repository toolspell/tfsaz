package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

// serveSSH accepts any user name, like the server, and only the client key
// it generates on start. It runs Git's upload-pack and receive-pack for
// /{collection}/{project}/_git/{repo}.
func (s *server) serveSSH(addr, keysDir string) error {
	host, err := newKey()
	if err != nil {
		return err
	}
	client, err := newKey()
	if err != nil {
		return err
	}
	if err := writeKeys(keysDir, host, client); err != nil {
		return err
	}
	allowed := client.PublicKey().Marshal()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(allowed, key.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown public key")
		},
	}
	cfg.AddHostKey(host)
	log.Printf("SSH host key %s, client key %s in %s", ssh.FingerprintSHA256(host.PublicKey()), ssh.FingerprintSHA256(client.PublicKey()), keysDir)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.sshConn(conn, cfg)
	}
}

type mockKey struct {
	ssh.Signer
	private ed25519.PrivateKey
}

func newKey() (mockKey, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return mockKey{}, err
	}
	signer, err := ssh.NewSignerFromKey(private)
	return mockKey{signer, private}, err
}

// writeKeys replaces the files in dir with this start's keys: the client
// private key and public key, and the host public key as a known_hosts
// pattern for any host. The files get the owner of dir, so the user who
// created it can use them from a bind mount.
func writeKeys(dir string, host, client mockKey) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("keys directory: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(client.private, "devops-server-mock client")
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"client_ed25519", pem.EncodeToMemory(block), 0o600},
		{"client_ed25519.pub", ssh.MarshalAuthorizedKey(client.PublicKey()), 0o644},
		{"known_hosts", append([]byte("* "), ssh.MarshalAuthorizedKey(host.PublicKey())...), 0o644},
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		_ = os.Remove(path)
		if err := os.WriteFile(path, f.data, f.mode); err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := os.Chown(path, int(st.Uid), int(st.Gid)); err != nil {
				log.Printf("chown %s: %v", path, err)
			}
		}
	}
	return nil
}

func (s *server) sshConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		log.Printf("SSH %s: %v", conn.RemoteAddr(), err)
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			return
		}
		go s.sshSession(ch, requests)
	}
}

func (s *server) sshSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()
	var env []string
	for r := range requests {
		switch r.Type {
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(r.Payload, &kv) == nil && kv.Name == "GIT_PROTOCOL" {
				env = append(env, "GIT_PROTOCOL="+kv.Value)
			}
			_ = r.Reply(true, nil)
		case "exec":
			var cmd struct{ Command string }
			if err := ssh.Unmarshal(r.Payload, &cmd); err != nil {
				_ = r.Reply(false, nil)
				return
			}
			_ = r.Reply(true, nil)
			status := s.sshExec(ch, cmd.Command, env)
			_, _ = ch.SendRequest("exit-status", false, binary.BigEndian.AppendUint32(nil, uint32(status)))
			return
		default:
			_ = r.Reply(false, nil)
		}
	}
}

func (s *server) sshExec(ch ssh.Channel, command string, env []string) int {
	log.Printf("SSH exec %s", command)
	verb, arg, _ := strings.Cut(command, " ")
	if verb == "git" {
		verb, arg, _ = strings.Cut(arg, " ")
		verb = "git-" + verb
	}
	if verb != "git-upload-pack" && verb != "git-receive-pack" {
		_, _ = fmt.Fprintf(ch.Stderr(), "unsupported command %q\n", command)
		return 1
	}
	rp := s.repoForSSHPath(shellUnquote(arg))
	if rp == nil {
		_, _ = fmt.Fprintln(ch.Stderr(), "remote: TF401019: The Git repository with name or identifier does not exist or you do not have permissions for the operation you are attempting.")
		return 128
	}
	cmd := exec.Command("git", strings.TrimPrefix(verb, "git-"), rp.Dir)
	cmd.Env = append(append(os.Environ(), gitIdentityEnv...), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = ch, ch, ch.Stderr()
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}

func shellUnquote(arg string) string {
	arg = strings.TrimSpace(arg)
	if strings.HasPrefix(arg, "'") && strings.HasSuffix(arg, "'") && len(arg) >= 2 {
		arg = strings.ReplaceAll(arg[1:len(arg)-1], `'\''`, "'")
	}
	return arg
}

func (s *server) repoForSSHPath(path string) *repo {
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[2], "_git") {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.collections {
		if strings.EqualFold(c.Name, parts[0]) {
			if p := c.project(parts[1]); p != nil {
				return c.repo(p, parts[3])
			}
		}
	}
	return nil
}
