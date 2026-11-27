package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type tool struct {
	root, binary string
}

func (l tool) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, l.binary, args...)
	cmd.Dir = l.root
	cmd.WaitDelay = 5 * time.Second
	cancelProcessTree(cmd)
	return cmd
}

func (l tool) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := l.command(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("live %s: %w: %s", commandLabel(args), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (l tool) json(ctx context.Context, timeout time.Duration, result any, args ...string) error {
	out, err := l.run(ctx, timeout, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("read JSON from live %s: %w", commandLabel(args), err)
	}
	return nil
}

func commandLabel(args []string) string {
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[:i]
	}
	return strings.Join(args, " ")
}
