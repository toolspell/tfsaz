package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const mockRequestsURL = "http://127.0.0.1:19090/requests"
const mockJournalHint = "check with go run ./tools/live services status"

func (e *env) mock(action, vm string, asJSON bool, out io.Writer) error {
	switch action {
	case "ca":
		path := filepath.Join(e.bed, "devops-server-mock", "keys", "ca.pem")
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("mock CA unavailable; run go run ./tools/live services up: %w", err)
		}
		_, err := fmt.Fprintln(out, path)
		return err
	case "requests":
		return e.mockRequests(mockRequestsURL, vm, asJSON, out)
	case "reset":
		resp, err := requestMockJournal(http.MethodDelete, mockRequestsURL, http.StatusNoContent)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	return fmt.Errorf("unknown mock action %q", action)
}

func (e *env) mockRequests(endpoint, vm string, asJSON bool, out io.Writer) error {
	if vm != "" {
		info, err := e.loadVM(vm)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(info.BedAddress)
		if err != nil {
			return fmt.Errorf("clone %s has no valid bed address", vm)
		}
		endpoint += "?ip=" + url.QueryEscape(ip.String())
	}
	resp, err := requestMockJournal(http.MethodGet, endpoint, http.StatusOK)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if asJSON {
		_, err := io.Copy(out, resp.Body)
		return err
	}
	var rows []struct {
		Time, ClientIP, Method, Scheme, Host, Path, Query, UserAgent, AuthScheme string
		Status                                                                   int
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return fmt.Errorf("decode mock journal GET response; %s: %w", mockJournalHint, err)
	}
	for _, row := range rows {
		target := row.Scheme + "://" + row.Host + row.Path
		if row.Query != "" {
			target += "?" + row.Query
		}
		if _, err := fmt.Fprintf(out, "%s %s %s %d %q auth=%q ua=%q\n",
			row.Time, row.ClientIP, row.Method, row.Status, target, row.AuthScheme, row.UserAgent); err != nil {
			return err
		}
	}
	return nil
}

func requestMockJournal(method, endpoint string, wantStatus int) (*http.Response, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build mock journal %s request: %w", method, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mock journal %s; %s: %w", method, mockJournalHint, err)
	}
	if resp.StatusCode != wantStatus {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("mock journal %s returned HTTP %d; %s", method, resp.StatusCode, mockJournalHint)
	}
	return resp, nil
}
