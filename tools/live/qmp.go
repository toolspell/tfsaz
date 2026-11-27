package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

// qmpSession is one connection to a VM's QEMU Machine Protocol socket.
type qmpSession struct {
	conn net.Conn
	r    *bufio.Reader
}

func dialQMP(socket string) (*qmpSession, error) {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("the VM does not run or has no QMP socket: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	s := &qmpSession{conn: conn, r: bufio.NewReader(conn)}
	if _, err := s.r.ReadString('\n'); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := s.execute("qmp_capabilities", nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

func (s *qmpSession) close() { _ = s.conn.Close() }

// execute runs one command and returns its result, skipping events.
func (s *qmpSession) execute(command string, arguments any) (json.RawMessage, error) {
	request := map[string]any{"execute": command}
	if arguments != nil {
		request["arguments"] = arguments
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(s.conn, string(data)+"\n"); err != nil {
		return nil, err
	}
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		var reply struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Desc string `json:"desc"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			return nil, err
		}
		if reply.Error != nil {
			return nil, fmt.Errorf("QEMU %s: %s", command, reply.Error.Desc)
		}
		if reply.Return != nil {
			return reply.Return, nil
		}
	}
}

// qmpScreendump asks QEMU to write the screen of a display device to file
// as a PNG.
func qmpScreendump(socket, device, file string) error {
	s, err := dialQMP(socket)
	if err != nil {
		return err
	}
	defer s.close()
	_, err = s.execute("screendump", map[string]string{"filename": file, "format": "png", "device": device})
	return err
}
