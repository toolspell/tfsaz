package az

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxInvokeParameters     = 6
	maxInvokeItemPathBytes  = 4096
	maxInvokeParameterBytes = 3*maxInvokeItemPathBytes + len("path=")
	maxBlobIDs              = 200 // Two object IDs for each of T3's 100 files.
	maxBlobInputBytes       = 16 << 10
	maxBlobArchiveBytes     = 64 << 20
)

var objectIDPattern = regexp.MustCompile(`(?i)^[a-f0-9]{40}$`)

type invokeRead struct {
	path    []string
	query   url.Values
	inFile  string
	outFile string
}

func parseInvoke(opts map[string]string) (invokeRead, error) {
	var read invokeRead
	if !strings.EqualFold(opts["area"], "git") || opts["api-version"] != gitReadVersion {
		return read, errors.New("devops invoke requires --area git and --api-version " + gitReadVersion)
	}
	var suffix string
	resource := strings.ToLower(opts["resource"])
	switch resource {
	case "pullrequestthreads":
		suffix = "threads"
	case "pullrequestlabels":
		suffix = "labels"
	case "pullrequestiterations", "pullrequestiterationchanges":
		suffix = "iterations"
	case "items", "blobs":
	default:
		return read, errors.New("unsupported devops invoke resource")
	}
	method := strings.ToUpper(opts["http-method"])
	accept := opts["accept-media-type"]
	if resource == "blobs" {
		if method != http.MethodPost || accept != "application/zip" ||
			!validInvokeText(opts["in-file"], maxInvokeItemPathBytes) || !validInvokeText(opts["out-file"], maxInvokeItemPathBytes) {
			return read, errors.New("blobs requires POST, application/zip, --in-file and --out-file")
		}
		read.inFile, read.outFile = opts["in-file"], opts["out-file"]
	} else if (method != "" && method != http.MethodGet) ||
		(accept != "" && accept != "application/json") || opts["in-file"] != "" || opts["out-file"] != "" {
		return read, errors.New("this devops invoke resource supports JSON GET only")
	}

	route, err := invokeParameters(opts["route-parameters"], "route")
	if err != nil {
		return read, err
	}
	query, err := invokeParameters(opts["query-parameters"], "query")
	if err != nil {
		return read, err
	}
	project, repo := route["project"], route["repositoryId"]
	for _, name := range []string{project, repo} {
		if !validInvokeText(name, 255) || !validSegment(name) {
			return read, errors.New("devops invoke requires valid project and repositoryId route parameters")
		}
	}
	read.path = []string{project, "_apis", "git", "repositories", repo}
	expectedRouteCount := 2
	if suffix != "" {
		prID, err := invokeNumber("pullRequestId", route["pullRequestId"], 1, 1<<31-1)
		if err != nil {
			return read, err
		}
		expectedRouteCount++
		read.path = append(read.path, "pullRequests", prID, suffix)
	} else {
		read.path = append(read.path, resource)
	}
	read.query = url.Values{"api-version": {gitReadVersion}}
	switch resource {
	case "pullrequestiterationchanges":
		iterationID, err := invokeNumber("iterationId", route["iterationId"], 1, 1<<31-1)
		if err != nil {
			return read, err
		}
		expectedRouteCount++
		read.path = append(read.path, iterationID, "changes")
		for key, value := range query {
			switch key {
			case "$skip":
				value, err = invokeNumber(key, value, 0, 1<<31-1)
			case "$top":
				value, err = invokeNumber(key, value, 1, 2000)
			default:
				return read, errors.New("unsupported iteration changes query parameter")
			}
			if err != nil {
				return read, err
			}
			read.query.Set(key, value)
		}
	case "items":
		if err := setInvokeItemQuery(read.query, query); err != nil {
			return read, err
		}
	default:
		if len(query) != 0 {
			return read, errors.New("this devops invoke resource takes no query parameters")
		}
	}
	if len(route) != expectedRouteCount {
		return read, errors.New("unexpected devops invoke route parameter")
	}
	return read, nil
}

func invokeParameters(raw, kind string) (map[string]string, error) {
	parameters := make(map[string]string)
	if raw == "" {
		return parameters, nil
	}
	for pair := range strings.SplitSeq(raw, "\x00") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("devops invoke %s parameters require key=value", kind)
		}
		if _, exists := parameters[key]; exists {
			return nil, fmt.Errorf("duplicate devops invoke %s parameter", kind)
		}
		parameters[key] = value
	}
	return parameters, nil
}

func invokeNumber(key, value string, min, max int64) (string, error) {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n < min || n > max {
		return "", fmt.Errorf("devops invoke %s must be an integer from %d to %d", key, min, max)
	}
	return strconv.FormatInt(n, 10), nil
}

func validInvokeText(s string, maxBytes int) bool {
	return s != "" && len(s) <= maxBytes && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func setInvokeItemQuery(query url.Values, params map[string]string) error {
	if len(params) != 6 || params["versionDescriptor.versionType"] != "commit" ||
		!objectIDPattern.MatchString(params["versionDescriptor.version"]) ||
		params["includeContent"] != "true" || params["includeContentMetadata"] != "true" || params["$format"] != "json" {
		return errors.New("items requires a commit, JSON format, content and content metadata")
	}
	// T3 percent-encodes the path before giving it to az. A literal plus
	// stays a plus, and a literal percent escape remains part of the filename.
	itemPath, err := url.PathUnescape(params["path"])
	if err != nil || !strings.HasPrefix(itemPath, "/") || !validInvokeText(itemPath, maxInvokeItemPathBytes) {
		return errors.New("items requires a valid absolute Git path")
	}
	for key, value := range params {
		query.Set(key, value)
	}
	query.Set("path", itemPath)
	return nil
}

func (read invokeRead) run(ctx context.Context, client *Client, cwd string, stdout io.Writer) error {
	if read.outFile != "" {
		return read.downloadBlobs(ctx, client, cwd)
	}
	resp, err := client.request(ctx, http.MethodGet, read.path, read.query, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := readResponse(resp.Body, maxResponseBytes)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return errors.New("Azure DevOps returned an invalid JSON object")
	}
	token := json.RawMessage("null")
	if header := resp.Header.Get("X-MS-ContinuationToken"); header != "" {
		token, _ = json.Marshal(header)
	}
	object["continuation_token"] = token
	return json.NewEncoder(stdout).Encode(object)
}

func invokeLocalPath(cwd, name string) string {
	if cwd != "" && !filepath.IsAbs(name) {
		return filepath.Join(cwd, name)
	}
	return name
}

func readBlobIDs(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBlobInputBytes {
		return nil, errors.New("--in-file must be a small regular JSON file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("could not open --in-file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("--in-file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBlobInputBytes+1))
	if err != nil || len(data) > maxBlobInputBytes {
		return nil, errors.New("could not read --in-file within the size limit")
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil || len(ids) == 0 || len(ids) > maxBlobIDs {
		return nil, fmt.Errorf("--in-file must contain an array of 1 to %d blob IDs", maxBlobIDs)
	}
	for _, id := range ids {
		if !objectIDPattern.MatchString(id) {
			return nil, errors.New("--in-file blob IDs must be 40 hexadecimal characters")
		}
	}
	return json.Marshal(ids)
}

func (read invokeRead) downloadBlobs(ctx context.Context, client *Client, cwd string) error {
	out := invokeLocalPath(cwd, read.outFile)
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("--out-file already exists")
		}
		return errors.New("could not check --out-file")
	}
	body, err := readBlobIDs(invokeLocalPath(cwd, read.inFile))
	if err != nil {
		return err
	}
	resp, err := client.request(ctx, http.MethodPost, read.path, read.query, "application/zip", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media != "application/zip" && media != "application/octet-stream" {
		return errors.New("Azure DevOps did not return a ZIP response")
	}
	data, err := readResponse(resp.Body, maxBlobArchiveBytes)
	if err != nil {
		return err
	}
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		return errors.New("Azure DevOps returned an invalid ZIP archive")
	}
	// The destination may have appeared during the request.
	file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("--out-file already exists")
		}
		return errors.New("could not create --out-file")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(out)
		return errors.New("could not write --out-file")
	}
	return nil
}
