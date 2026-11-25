package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const genericRESTTimeout = 5 * time.Minute

// These requests have T3-specific response or file handling. Other invocations
// resolve their resource at the selected collection when the command runs.
func stockOutput(opts map[string]string) bool {
	return (opts["output"] == "" || opts["output"] == "json") && opts["encoding"] == ""
}

type apiCall struct {
	request                 genericRequest
	area, resource, version string
	route                   map[string]string
	query                   url.Values
}

func parseAPICall(command string, opts map[string]string, cwd string) (*apiCall, error) {
	call := &apiCall{area: opts["area"], resource: opts["resource"], version: opts["api-version"]}
	invoke := command == "devops invoke"
	method, outFile, queryParams := opts["method"], opts["output-file"], opts["uri-parameters"]
	if invoke {
		method, outFile, queryParams = opts["http-method"], opts["out-file"], opts["query-parameters"]
		if call.resource != "" && call.area == "" {
			return nil, errors.New("--resource requires --area")
		}
		if call.resource == "" && (method != "" || opts["route-parameters"] != "" || queryParams != "" ||
			opts["in-file"] != "" || outFile != "" || call.version != "" || opts["media-type"] != "" || opts["accept-media-type"] != "") {
			return nil, errors.New("endpoint discovery takes an optional --area, --query and output format")
		}
		if call.version != "" && !validAPIVersion(call.version) {
			return nil, errors.New("invalid --api-version")
		}
		if v := opts["encoding"]; v != "" && !strings.EqualFold(v, "utf-8") && !strings.EqualFold(v, "utf8") {
			return nil, errors.New("request files require UTF-8 encoding")
		}
	} else if opts["url"] == "" {
		return nil, errors.New("rest requires --url")
	}
	r := &call.request
	r.method = strings.ToUpper(method)
	switch r.method {
	case "":
		r.method = http.MethodGet
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, errors.New("unsupported HTTP method")
	}
	var err error
	call.route, err = invokeParameters(opts["route-parameters"], "route")
	if err != nil {
		return nil, err
	}
	// Path values are bound by the catalog resolver. Reject traversal before
	// consulting the server even for catch-all slots, which may contain '/'.
	for _, value := range call.route {
		for _, part := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
			if !validSegment(part) {
				return nil, errors.New("invalid REST route parameter")
			}
		}
	}
	params, err := apiParameters(queryParams, "query", cwd, !invoke)
	if err != nil {
		return nil, err
	}
	call.query = make(url.Values, len(params))
	for key, value := range params {
		call.query.Set(key, value)
	}
	r.mediaType, r.accept = opts["media-type"], opts["accept-media-type"]
	if headers := opts["headers"]; headers != "" {
		values, err := apiParameters(headers, "header", cwd, true)
		if err != nil {
			return nil, err
		}
		for name, value := range values {
			name = http.CanonicalHeaderKey(name)
			if !validInvokeText(value, 8192) {
				return nil, errors.New("invalid REST header value")
			}
			switch name {
			case "Accept":
				r.accept = value
			case "Content-Type":
				r.mediaType = value
			case "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "Range", "Prefer":
				if r.headers == nil {
					r.headers = make(http.Header)
				}
				r.headers.Set(name, value)
			default:
				return nil, errors.New("unsupported REST request header")
			}
		}
	}
	for _, media := range []string{r.accept, r.mediaType} {
		if media != "" {
			if _, _, err := mime.ParseMediaType(media); err != nil || !validInvokeText(media, 1024) {
				return nil, errors.New("invalid HTTP media type")
			}
		}
	}
	body, file := opts["body"], opts["in-file"]
	if !invoke && strings.HasPrefix(body, "@") {
		file, body = body[1:], ""
		if file == "" {
			return nil, errors.New("--body @file requires a filename")
		}
	}
	if file != "" || body != "" {
		if r.method == http.MethodGet || r.method == http.MethodHead || r.method == http.MethodOptions {
			return nil, errors.New("this HTTP method does not accept a request body")
		}
		const maxInput = 32 << 20
		if file != "" {
			r.body, err = readInvokeInput(invokeLocalPath(cwd, file), maxInput)
			if err != nil {
				return nil, err
			}
		} else {
			r.body = []byte(body)
		}
		if len(r.body) > maxInput || (invoke && !json.Valid(r.body)) {
			return nil, errors.New("request body must contain valid JSON within the size limit")
		}
	}
	if outFile != "" {
		if opts["query"] != "" || (opts["output"] != "" && opts["output"] != "json" && opts["output"] != "none") {
			return nil, errors.New("a raw output file cannot be combined with a query or formatted output")
		}
		r.outFile = invokeLocalPath(cwd, outFile)
	}
	r.discard = opts["output"] == "none"
	r.renderJSON, err = apiRenderer(opts["query"], opts["output"])
	return call, err
}

func (call *apiCall) invoke(ctx context.Context, c *Client, scope Scope, stdout io.Writer) error {
	c.http.Timeout = genericRESTTimeout
	locations, err := fetchCatalog(ctx, c)
	if err != nil {
		return err
	}
	if call.resource == "" {
		rows := make([]json.RawMessage, 0, len(locations))
		for _, location := range locations {
			if call.area == "" || strings.EqualFold(call.area, location.Area) {
				rows = append(rows, location.raw)
			}
		}
		if call.request.renderJSON == nil {
			return json.NewEncoder(stdout).Encode(rows)
		}
		raw, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		return call.request.renderJSON(stdout, raw)
	}
	path, version, err := resolveResource(locations, call.area, call.resource, call.version, call.route)
	if err != nil {
		return usageError{err}
	}
	// The extension negotiates the media version in Accept; some endpoints
	// additionally expect a preview override in the query string.
	if call.request.accept == "" {
		call.request.accept = "application/json"
	}
	media, params, err := mime.ParseMediaType(call.request.accept)
	if err != nil {
		return usageError{errors.New("invalid response media type")}
	}
	params["api-version"] = version
	call.request.accept = mime.FormatMediaType(media, params)
	if strings.EqualFold(call.area, "git") && strings.EqualFold(call.resource, "items") {
		// Stock T3 passes an encoded absolute Git path, even for newer API versions.
		if raw := call.query.Get("path"); strings.HasPrefix(strings.ToLower(raw), "%2f") {
			path, err := url.PathUnescape(raw)
			if err != nil {
				return usageError{errors.New("invalid encoded Git item path")}
			}
			call.query.Set("path", path)
		}
	}
	if strings.EqualFold(call.area, "wit") {
		call.request.normalizeObject = func(obj map[string]json.RawMessage, header http.Header) error {
			switch strings.ToLower(call.resource) {
			case "workitemsbatch":
				return rewriteWorkItemBatchLinks(obj, scope.Organization)
			case "comments":
				setCommentCursor(obj, header.Get("X-MS-ContinuationToken"))
			}
			return nil
		}
	}
	return call.request.run(ctx, c, path, call.query, stdout, true)
}

func (a app) runRest(ctx context.Context, cfg config, call *apiCall, rawURL string) error {
	p, path, query, err := cfg.restTarget(ctx, a.cwd, rawURL)
	if err != nil {
		return usageError{err}
	}
	for key, values := range call.query {
		query[key] = values
	}
	c, err := a.client(*p)
	if err != nil {
		return err
	}
	c.http.Timeout = genericRESTTimeout
	return call.request.run(ctx, c, path, query, a.stdout, false)
}

// az rest also accepts its header and URI parameter dictionaries as a JSON
// object or @file. devops invoke uses individual key=value arguments.
func apiParameters(raw, kind, cwd string, allowJSON bool) (map[string]string, error) {
	if !allowJSON || (!strings.HasPrefix(strings.TrimSpace(raw), "{") && !strings.HasPrefix(raw, "@")) {
		return invokeParameters(raw, kind)
	}
	data := []byte(raw)
	if strings.HasPrefix(raw, "@") {
		var err error
		data, err = readInvokeInput(invokeLocalPath(cwd, raw[1:]), 1<<20)
		if err != nil {
			return nil, err
		}
	}
	var values map[string]string
	if json.Unmarshal(data, &values) != nil || values == nil {
		return nil, fmt.Errorf("REST %s parameters require a JSON object with string values", kind)
	}
	return values, nil
}
