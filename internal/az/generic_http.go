package az

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type genericRequest struct {
	headers         http.Header
	method          string
	accept          string
	mediaType       string
	outFile         string
	body            []byte
	discard         bool
	normalizeObject func(map[string]json.RawMessage, http.Header) error
	renderJSON      func(io.Writer, json.RawMessage) error
}

func (r genericRequest) run(ctx context.Context, c *Client, path []string, query url.Values, stdout io.Writer, invoke bool) (err error) {
	if r.method == "" {
		r.method = http.MethodGet
	}
	if r.accept == "" {
		r.accept = "application/json"
	}
	var body io.Reader
	if r.body != nil {
		if invoke && !json.Valid(r.body) {
			return errors.New("request body must be valid JSON")
		}
		body = bytes.NewReader(r.body)
		if r.mediaType == "" {
			if json.Valid(r.body) {
				r.mediaType = "application/json"
			} else {
				r.mediaType = "text/plain; charset=utf-8"
			}
		}
	}

	var staged *os.File
	if r.outFile != "" {
		var err error
		staged, err = stageGenericOutput(r.outFile)
		if err != nil {
			return err
		}
		defer removeStaged(staged.Name(), &err)
		defer closeQuietly(staged)
	}
	resp, err := c.requestWithMedia(ctx, r.method, path, query, r.accept, r.mediaType, r.headers, body)
	if err != nil {
		return err
	}
	defer closeQuietly(resp.Body)
	if r.method != http.MethodHead && r.method != http.MethodGet && r.method != http.MethodOptions {
		defer func() {
			if err != nil {
				err = fmt.Errorf("Azure DevOps returned HTTP %d for %s, but processing its response failed: %w", resp.StatusCode, r.method, err)
			}
		}()
	}
	if staged != nil {
		// az rest creates the requested file even for HEAD and 204. The
		// DevOps extension only saves responses that declare a content type.
		if invoke && resp.Header.Get("Content-Type") == "" {
			return nil
		}
		return publishGenericOutput(staged, r.outFile, resp.Body)
	}
	if r.method == http.MethodHead || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if r.discard {
		return nil
	}
	data, err := readResponse(resp.Body, maxResponseBytes)
	if err != nil {
		return err
	}
	raw := data
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}
	if !json.Valid(data) {
		if !invoke && r.renderJSON == nil && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/") && utf8.Valid(raw) {
			_, err = stdout.Write(raw)
			return err
		}
		return errors.New("Azure DevOps did not return valid JSON; save the raw response to an output file")
	}
	if invoke && data[0] == '{' {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return errors.New("Azure DevOps returned an invalid JSON object")
		}
		token := json.RawMessage("null")
		if header := resp.Header.Get("X-MS-ContinuationToken"); header != "" {
			token, _ = json.Marshal(header)
		}
		object["continuation_token"] = token
		if r.normalizeObject != nil {
			if err := r.normalizeObject(object, resp.Header); err != nil {
				return err
			}
		}
		data, err = json.Marshal(object)
		if err != nil {
			return errors.New("could not encode the Azure DevOps response")
		}
	}
	if r.renderJSON != nil {
		return r.renderJSON(stdout, data)
	}
	return json.NewEncoder(stdout).Encode(json.RawMessage(data))
}

func stageGenericOutput(out string) (*os.File, error) {
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return nil, errors.New("--out-file already exists")
		}
		return nil, errors.New("could not check --out-file")
	}
	file, err := os.CreateTemp(filepath.Dir(out), ".tfsaz-response-*")
	if err != nil {
		return nil, errors.New("could not stage --out-file")
	}
	return file, nil
}

func publishGenericOutput(file *os.File, out string, body io.Reader) error {
	_, writeErr := io.Copy(file, body)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return errors.New("could not write --out-file")
	}
	if err := os.Link(file.Name(), out); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("--out-file already exists")
		}
		return errors.New("could not publish --out-file")
	}
	return nil
}
