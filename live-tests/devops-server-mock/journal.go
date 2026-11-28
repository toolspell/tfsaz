package main

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const journalCapacity = 5000

type requestEntry struct {
	Time       time.Time `json:"time"`
	ClientIP   string    `json:"clientIP"`
	Method     string    `json:"method"`
	Scheme     string    `json:"scheme"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	Query      string    `json:"query"`
	Status     int       `json:"status"`
	UserAgent  string    `json:"userAgent"`
	AuthScheme string    `json:"authScheme"`
}

type requestJournal struct {
	mu      sync.Mutex
	entries []requestEntry
	next    int
}

func (j *requestJournal) record(entry requestEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.entries) < journalCapacity {
		j.entries = append(j.entries, entry)
		return
	}
	j.entries[j.next] = entry
	j.next = (j.next + 1) % journalCapacity
}

func (j *requestJournal) requests(ip string) []requestEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	rows := make([]requestEntry, 0, len(j.entries))
	for i := range j.entries {
		entry := j.entries[(j.next+i)%len(j.entries)]
		if ip == "" || entry.ClientIP == ip {
			rows = append(rows, entry)
		}
	}
	return rows
}

func (j *requestJournal) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, j.requests(r.URL.Query().Get("ip")))
	})
	mux.HandleFunc("DELETE /requests", func(w http.ResponseWriter, _ *http.Request) {
		j.mu.Lock()
		j.entries, j.next = nil, 0
		j.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func newRequestEntry(r *http.Request, pat string) requestEntry {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	header := r.Header.Get("Authorization")
	_, authValue, _ := strings.Cut(header, " ")
	_, password, _ := r.BasicAuth()
	var replacements []string
	for _, secret := range []string{pat, strings.TrimSpace(authValue), password} {
		if secret != "" {
			replacements = append(replacements, secret, "[redacted]", url.PathEscape(secret), "[redacted]", url.QueryEscape(secret), "[redacted]")
		}
	}
	redact := strings.NewReplacer(replacements...).Replace
	return requestEntry{
		Time: time.Now().UTC(), ClientIP: ip, Method: r.Method,
		Scheme: requestScheme(r), Host: redact(r.Host),
		Path: journalPath(r.URL, redact), Query: journalQuery(r.URL.RawQuery, redact),
		UserAgent: redact(r.UserAgent()), AuthScheme: authScheme(header),
	}
}

func authScheme(header string) string {
	scheme, _, _ := strings.Cut(strings.TrimSpace(header), " ")
	switch strings.ToLower(scheme) {
	case "basic":
		return "Basic"
	case "bearer":
		return "Bearer"
	case "digest":
		return "Digest"
	case "negotiate":
		return "Negotiate"
	case "ntlm":
		return "NTLM"
	case "":
		return ""
	default:
		return "other"
	}
}

// journalPath redacts a secret in the path even when the client encoded
// some of its characters, such as %61 for a.
func journalPath(u *url.URL, redact func(string) string) string {
	if decoded := u.Path; redact(decoded) != decoded {
		return redact(decoded)
	}
	return redact(u.EscapedPath())
}

func journalQuery(query string, redact func(string) string) string {
	pairs := strings.Split(query, "&")
	for i, pair := range pairs {
		rawName, rawValue, hasValue := strings.Cut(pair, "=")
		name, secretName := redactPart(rawName, redact)
		value, secretValue := redactPart(rawValue, redact)
		switch decoded, _ := url.QueryUnescape(rawName); strings.ToLower(decoded) {
		case "authorization", "access_token", "api_key", "client_secret", "password", "pat", "sig", "token":
			secretValue = true
		}
		if secretValue {
			value = "[redacted]"
		}
		if secretName {
			name = "[redacted]"
		}
		pairs[i] = name
		if hasValue {
			pairs[i] += "=" + value
		}
	}
	// A secret with = or & in it spans pairs.
	joined := strings.Join(pairs, "&")
	if decoded := lenientUnescape(joined); redact(decoded) != decoded {
		return "[redacted]"
	}
	return redact(joined)
}

// redactPart reports whether a query name or value holds a secret, raw or
// decoded, such as %61 for a, and returns it otherwise unchanged.
func redactPart(raw string, redact func(string) string) (string, bool) {
	if redact(raw) != raw {
		return raw, true
	}
	if decoded := lenientUnescape(raw); redact(decoded) != decoded {
		return raw, true
	}
	return raw, false
}

// lenientUnescape decodes a query the way a server would read it, but keeps
// an invalid escape such as %ZZ as it is, so that one bad escape cannot
// hide a secret elsewhere in the same text.
func lenientUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '+':
			b.WriteByte(' ')
		case s[i] == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			i += 2
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	}
	return c - 'a' + 10
}

type journalResponse struct {
	http.ResponseWriter
	status int
}

func (w *journalResponse) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *journalResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}
