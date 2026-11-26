package az

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// A successful mutation can have an unreadable response. Report that separately
// so callers can inspect the resource before deciding whether to retry.
func writeJSON(ctx context.Context, c *Client, method string, path []string, query url.Values, media string, body any, dest any) error {
	var input io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(payload)
	}
	resp, err := c.requestWithMedia(ctx, method, path, query, "application/json", media, nil, input)
	if err != nil {
		return c.permissionError(ctx, err)
	}
	defer closeQuietly(resp.Body)
	if dest == nil {
		return nil
	}
	data, err := readResponse(resp.Body, maxResponseBytes)
	if err != nil || json.Unmarshal(data, dest) != nil {
		return fmt.Errorf("Azure DevOps returned HTTP %d for %s but its response could not be read; check the resource before retrying", resp.StatusCode, method)
	}
	return nil
}

func (a app) expandWriteText(opts map[string]string) error {
	usedStdin := false
	expand := func(value string) (string, error) {
		if !strings.HasPrefix(value, "@") {
			return value, nil
		}
		const limit = 4 << 20
		var reader io.Reader
		if value == "@-" {
			if usedStdin || a.stdin == nil {
				return "", errors.New("only one text value may read stdin")
			}
			usedStdin = true
			reader = a.stdin
		} else {
			name := strings.TrimPrefix(value, "@")
			if !filepath.IsAbs(name) {
				name = filepath.Join(a.cwd, name)
			}
			info, err := os.Stat(name)
			if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
				return "", errors.New("text option requires a readable UTF-8 file of at most 4 MiB")
			}
			file, err := os.Open(name)
			if err != nil {
				return "", errors.New("cannot open text input file")
			}
			defer closeQuietly(file)
			reader = file
		}
		data, err := io.ReadAll(io.LimitReader(reader, limit+1))
		if err != nil || len(data) > limit || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return "", errors.New("invalid UTF-8 text input or text input exceeds 4 MiB")
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	for _, key := range []string{"title", "description", "merge-commit-message"} {
		if v, ok := opts[key]; ok {
			out, err := expand(v)
			if err != nil {
				return err
			}
			opts[key] = out
		}
	}
	if raw, ok := opts["fields"]; ok {
		pairs := strings.Split(raw, "\x00")
		for i, pair := range pairs {
			key, v, ok := strings.Cut(pair, "=")
			if !ok {
				return errors.New("--fields requires field=value pairs")
			}
			out, err := expand(v)
			if err != nil {
				return err
			}
			pairs[i] = key + "=" + out
		}
		opts["fields"] = strings.Join(pairs, "\x00")
	}
	return nil
}
