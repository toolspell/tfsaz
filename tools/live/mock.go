package main

import (
	"encoding/json"
	"flag"
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

func (e *env) mock(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "ca":
		if len(args) != 1 {
			return errUsage
		}
		path := filepath.Join(e.bed, "devops-server-mock", "keys", "ca.pem")
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("mock CA unavailable; run go run ./tools/live services up: %w", err)
		}
		_, err := fmt.Fprintln(out, path)
		return err
	case "requests":
		flags := flag.NewFlagSet("mock requests", flag.ContinueOnError)
		vm := flags.String("vm", "", "filter by clone name")
		asJSON := flags.Bool("json", false, "print JSON")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
			return errUsage
		}
		return e.mockRequests(mockRequestsURL, *vm, *asJSON, out)
	case "reset":
		if len(args) != 1 {
			return errUsage
		}
		resp, err := requestMockJournal(http.MethodDelete, mockRequestsURL, http.StatusNoContent)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	return errUsage
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
		return fmt.Errorf("read mock journal: %w", err)
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
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mock journal: %w", err)
	}
	if resp.StatusCode != wantStatus {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("mock journal: %s", resp.Status)
	}
	return resp, nil
}
