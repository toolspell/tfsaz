package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestJournal(t *testing.T) {
	s := &server{cfg: config{pat: "seed-value"}, collections: []*collection{{Name: "DefaultCollection"}}}
	const path = "/tfs/DefaultCollection/_apis"
	r := httptest.NewRequest(http.MethodOptions, "https://devops.mock.test:8443"+path+"?allHostTypes=true&%50assword=query-value&other=attempt-value&encoded=%61ttempt-value&attempt-value=visible&%61ttempt-value=%ZZ", nil)
	r.RemoteAddr = "172.30.53.129:12345"
	r.Header.Set("X-Forwarded-For", "172.30.53.130")
	r.Header.Set("User-Agent", "journal-test seed-value")
	r.SetBasicAuth("tester", "attempt-value")
	s.ServeHTTP(httptest.NewRecorder(), r)

	r2 := httptest.NewRequest(http.MethodGet, "http://devops.mock.test:8080/", nil)
	r2.RemoteAddr = "172.30.53.130:54321"
	s.ServeHTTP(httptest.NewRecorder(), r2)

	r3 := httptest.NewRequest(http.MethodGet, "http://devops.mock.test:8080/%61ttempt-value", nil)
	r3.RemoteAddr = "172.30.53.131:23456"
	r3.SetBasicAuth("tester", "attempt-value")
	s.ServeHTTP(httptest.NewRecorder(), r3)

	admin := s.journal.adminHandler()
	get := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/requests"+query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET /requests%s = %d", query, w.Code)
		}
		return w
	}
	w := get("?ip=172.30.53.129")
	for _, secret := range []string{"seed-value", "query-value", "attempt-value", r.Header.Get("Authorization"), "172.30.53.130"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Error("filtered journal contains a credential or the other client")
		}
	}
	var rows []requestEntry
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("filtered journal contains %d rows; want 1", len(rows))
	}
	got := rows[0]
	if got.Time.IsZero() || got.ClientIP != "172.30.53.129" || got.Method != "OPTIONS" ||
		got.Scheme != "https" || got.Host != "devops.mock.test:8443" || got.Path != path ||
		got.Query != "allHostTypes=true&%50assword=[redacted]&other=[redacted]&encoded=[redacted]&[redacted]=visible&[redacted]=%ZZ" ||
		got.Status != http.StatusUnauthorized || got.UserAgent != "journal-test [redacted]" || got.AuthScheme != "Basic" {
		t.Errorf("unexpected journal entry: %+v", got)
	}
	r4 := httptest.NewRequest(http.MethodGet, "http://devops.mock.test:8080/?password=visible", nil)
	r4.RemoteAddr = "172.30.53.132:34567"
	r4.SetBasicAuth("tester", "password")
	s.ServeHTTP(httptest.NewRecorder(), r4)
	var named []requestEntry
	if err := json.Unmarshal(get("?ip=172.30.53.132").Body.Bytes(), &named); err != nil || len(named) != 1 || named[0].Query != "[redacted]=[redacted]" {
		t.Errorf("a password used as a query name stays in the journal: %+v, %v", named, err)
	}
	var encoded []requestEntry
	if err := json.Unmarshal(get("?ip=172.30.53.131").Body.Bytes(), &encoded); err != nil || len(encoded) != 1 || encoded[0].Path != "/[redacted]" {
		t.Errorf("a percent-encoded secret in the path stays in the journal: %+v, %v", encoded, err)
	}
	w = httptest.NewRecorder()
	admin.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/requests", nil))
	if w.Code != http.StatusNoContent || strings.TrimSpace(get("").Body.String()) != "[]" {
		t.Fatalf("reset status = %d, journal = %s; want 204 and an empty array", w.Code, get("").Body)
	}
}

func TestJournalCapacity(t *testing.T) {
	var journal requestJournal
	for i := range journalCapacity + 2 {
		journal.record(requestEntry{Time: time.Unix(int64(i), 0)})
	}
	rows := journal.requests("")
	if len(rows) != journalCapacity || rows[0].Time.Unix() != 2 || rows[len(rows)-1].Time.Unix() != journalCapacity+1 {
		t.Fatal("journal did not retain the newest entries in order")
	}
}
