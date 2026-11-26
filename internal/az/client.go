package az

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const (
	maxResponseBytes = 32 << 20
	maxErrorBytes    = 64 << 10
	loginHint        = "Run 'az devops login' for this collection."
)

var serviceErrorCode = regexp.MustCompile(`\b(?:tf|vs)[0-9]{4,8}\b`)

var errRejectedPAT = errors.New("Azure DevOps rejected the PAT")

// Client makes authenticated REST requests to a single collection.
type Client struct {
	collection    url.URL
	pat           string
	http          *http.Client
	checkBoundary func([]string) error
}

func newClient(profile Profile, pat string) (*Client, error) {
	if strings.TrimSpace(pat) == "" {
		return nil, fmt.Errorf("no PAT is configured. %s", loginHint)
	}
	collection, err := url.Parse(profile.CollectionURL)
	if err != nil || collection.Scheme != "https" || collection.Hostname() == "" ||
		collection.User != nil || collection.ForceQuery || collection.RawQuery != "" || collection.Fragment != "" {
		return nil, errors.New("invalid HTTPS collection URL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if profile.CAFile != "" {
		pem, err := os.ReadFile(profile.CAFile)
		if err != nil {
			return nil, errors.New("could not read the collection CA file")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("could not load the system TLS trust store")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("the collection CA file contains no PEM certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	}
	return &Client{
		collection: *collection,
		pat:        pat,
		http: &http.Client{
			Transport: transport,
			Timeout:   25 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Get appends individual REST path segments to the collection and decodes JSON.
func (c *Client) Get(ctx context.Context, path []string, query url.Values, dest any) error {
	resp, err := c.request(ctx, http.MethodGet, path, query, "application/json", nil)
	if err != nil {
		return err
	}
	defer closeQuietly(resp.Body)
	body, err := readResponse(resp.Body, maxResponseBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return errors.New("Azure DevOps returned an invalid JSON response")
	}
	return nil
}

func (c *Client) request(ctx context.Context, method string, path []string, query url.Values, accept string, body io.Reader) (*http.Response, error) {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	return c.requestWithMedia(ctx, method, path, query, accept, contentType, nil, body)
}

func (c *Client) requestWithMedia(ctx context.Context, method string, path []string, query url.Values, accept, contentType string, headers http.Header, body io.Reader) (*http.Response, error) {
	if len(path) == 0 {
		return nil, errors.New("missing Azure DevOps REST path")
	}
	if c.checkBoundary != nil {
		if err := c.checkBoundary(path); err != nil {
			return nil, usageError{err}
		}
	}
	target := c.collection
	escaped := strings.TrimRight(target.EscapedPath(), "/")
	target.Path = strings.TrimRight(target.Path, "/")
	for _, segment := range path {
		if !validSegment(segment) {
			return nil, errors.New("invalid Azure DevOps REST path segment")
		}
		target.Path += "/" + segment
		escaped += "/" + url.PathEscape(segment)
	}
	target.RawPath = escaped
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, errors.New("could not construct the Azure DevOps REST request")
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Accept", accept)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-TFS-FedAuthRedirect", "Suppress")
	req.SetBasicAuth("", c.pat)
	start := time.Now()
	resp, err := c.http.Do(req)
	if trace := getCommandTrace(ctx); trace != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		trace.record(status, start)
	}
	if err != nil {
		failure := collectionRequestError(err)
		switch method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			return nil, fmt.Errorf("%w; the request outcome is unknown, check the resource before retrying", failure)
		}
		return nil, failure
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.StatusCode == http.StatusNonAuthoritativeInfo {
		defer closeQuietly(resp.Body)
		return nil, collectionResponseError(resp, path)
	}
	return resp, nil
}

func readResponse(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, collectionRequestError(err)
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("Azure DevOps response exceeded the size limit")
	}
	return body, nil
}

// Server can return 401 for an otherwise valid PAT missing permission
// for an operation. Verify the credential at the same collection before asking
// the caller to log in again.
func (c *Client) permissionError(ctx context.Context, err error) error {
	if !errors.Is(err, errRejectedPAT) {
		return err
	}
	if _, probeErr := c.authenticatedUser(ctx); probeErr != nil {
		return probeErr
	}
	return errors.New("Azure DevOps authenticated the PAT but denied this operation. Check the PAT's scopes and the account's project permissions")
}

func collectionRequestError(err error) error {
	var requestError *url.Error
	if errors.As(err, &requestError) {
		err = requestError.Err
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("Azure DevOps collection request: %w", context.Canceled)
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return errors.New("Azure DevOps collection request timed out")
	}
	var certificateError *tls.CertificateVerificationError
	var recordError tls.RecordHeaderError
	if errors.As(err, &certificateError) || errors.As(err, &recordError) || errors.Is(err, http.ErrSchemeMismatch) ||
		strings.HasPrefix(err.Error(), "remote error: tls:") || strings.HasPrefix(err.Error(), "tls:") {
		return errors.New("TLS verification or handshake with the Azure DevOps collection failed")
	}
	return errors.New("could not reach the Azure DevOps collection")
}

func collectionResponseError(resp *http.Response, path []string) error {
	status := resp.StatusCode
	switch {
	case status == http.StatusUnauthorized || status == http.StatusNonAuthoritativeInfo:
		return fmt.Errorf("%w (HTTP %d). %s", errRejectedPAT, status, loginHint)
	case status >= 300 && status < 400:
		return fmt.Errorf("Azure DevOps collection returned a redirect (HTTP %d)", status)
	case status == http.StatusTooManyRequests:
		return errors.New("Azure DevOps rate limit exceeded (HTTP 429)")
	}

	// Recognize stable server error markers without surfacing response text.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	message := strings.ToLower(string(body))
	if strings.Contains(message, "tf400733") {
		return errors.New("Azure DevOps rate limit exceeded (TF400733)")
	}
	if status == http.StatusForbidden {
		return errors.New("Azure DevOps denied access (HTTP 403 Forbidden)")
	}
	if status == http.StatusNotFound && len(path) == 4 && path[0] == "_apis" && path[1] == "wit" && path[2] == "workItems" {
		return errors.New("work item not found or not readable (HTTP 404)")
	}
	prByID := len(path) >= 2 && strings.EqualFold(path[len(path)-2], "pullRequests")
	if strings.Contains(message, "tf401180") || strings.Contains(message, "gitpullrequestnotfoundexception") ||
		(strings.Contains(message, "pull request") && (strings.Contains(message, "not found") || strings.Contains(message, "does not exist"))) ||
		(prByID && status == http.StatusNotFound) {
		return errors.New("pull request not found")
	}
	// Server messages can echo request bodies and user data. A stable TF or
	// VS code identifies the error without exposing that text.
	if code := serviceErrorCode.FindString(message); code != "" {
		return fmt.Errorf("Azure DevOps collection request failed (HTTP %d, %s)", status, strings.ToUpper(code))
	}
	return fmt.Errorf("Azure DevOps collection request failed (HTTP %d)", status)
}

// Identity is the authenticated Azure DevOps user's ID and login name.
type Identity struct {
	ID    string
	Login string
}

type collectionIdentity struct {
	ID          string `json:"id"`
	IsContainer bool   `json:"isContainer"`
	Descriptor  string `json:"descriptor"`
	UniqueName  string `json:"uniqueName"`
	Properties  struct {
		Account json.RawMessage `json:"Account"`
		Domain  json.RawMessage `json:"Domain"`
	} `json:"properties"`
}

const identityAPIVersion = "5.0"

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (user collectionIdentity) isUser() bool {
	return !user.IsContainer && guidPattern.MatchString(user.ID) &&
		!strings.EqualFold(user.ID, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa") &&
		user.ID != "00000000-0000-0000-0000-000000000000"
}

func (c *Client) authenticatedUser(ctx context.Context) (collectionIdentity, error) {
	var connection struct {
		AuthenticatedUser collectionIdentity `json:"authenticatedUser"`
	}
	if err := c.Get(ctx, []string{"_apis", "connectionData"}, url.Values{"api-version": {"5.0-preview.1"}}, &connection); err != nil {
		return collectionIdentity{}, err
	}
	user := connection.AuthenticatedUser
	if !user.isUser() {
		return collectionIdentity{}, fmt.Errorf("Azure DevOps did not authenticate a PAT user. %s", loginHint)
	}
	return user, nil
}

// Identity verifies the credential against the configured collection itself.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	user, err := c.authenticatedUser(ctx)
	if err != nil {
		return Identity{}, err
	}
	login := user.login()
	if !qualifiedLogin(login) {
		var identities struct {
			Value []collectionIdentity `json:"value"`
		}
		query := url.Values{"api-version": {identityAPIVersion}, "identityIds": {user.ID}}
		if err := c.Get(ctx, []string{"_apis", "identities"}, query, &identities); err != nil {
			// Qualification is optional when connectionData already verified a login.
			if login != "" && !errors.Is(err, errRejectedPAT) && !errors.Is(ctx.Err(), context.Canceled) {
				return Identity{ID: user.ID, Login: login}, nil
			}
			return Identity{}, fmt.Errorf("look up the authenticated Azure DevOps user: %w", err)
		}
		if len(identities.Value) == 1 {
			match := identities.Value[0]
			// Server can return a master identity ID. Its descriptor still
			// identifies the same principal; keep the collection ID for filters.
			sameDescriptor := user.Descriptor != "" && user.Descriptor == match.Descriptor
			if match.isUser() && (strings.EqualFold(match.ID, user.ID) || sameDescriptor) {
				if resolved := match.login(); login == "" || qualifiedLogin(resolved) {
					login = resolved
				}
			}
		}
	}
	if login == "" {
		return Identity{}, errors.New("Azure DevOps did not return a login for the authenticated user")
	}
	return Identity{ID: user.ID, Login: login}, nil
}

func (user collectionIdentity) login() string {
	account := identityProperty(user.Properties.Account)
	if qualifiedLogin(account) {
		return account
	}
	domain := identityProperty(user.Properties.Domain)
	if validLoginPart(domain) && validLoginPart(account) {
		return domain + `\` + account
	}
	if qualifiedLogin(user.UniqueName) || validLoginPart(user.UniqueName) {
		return user.UniqueName
	}
	if validLoginPart(account) {
		return account
	}
	return ""
}

func identityProperty(raw json.RawMessage) string {
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	var typed struct {
		Value string `json:"$value"`
	}
	if json.Unmarshal(raw, &typed) == nil {
		return typed.Value
	}
	return ""
}

func qualifiedLogin(login string) bool {
	if domain, account, ok := strings.Cut(login, `\`); ok {
		return validLoginPart(domain) && validLoginPart(account)
	}
	account, domain, ok := strings.Cut(login, "@")
	return ok && validLoginPart(account) && validLoginPart(domain)
}

func validLoginPart(part string) bool {
	return part != "" && part != "." && part != ".." && len(part) <= 256 &&
		!strings.ContainsAny(part, `\/@<>:"`) &&
		strings.TrimSpace(part) == part && !strings.ContainsFunc(part, unicode.IsControl)
}
