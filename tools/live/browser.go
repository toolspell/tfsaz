package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func browserStartCommand(osName, browser string, port int, url string) string {
	debug := fmt.Sprintf("--remote-debugging-port=%d", port)
	profile := fmt.Sprintf("tfsaz-live-%s-%d", browser, port)
	switch osName {
	case "windows":
		executable := `"$env:ProgramFiles\Google\Chrome\Application\chrome.exe"`
		if browser == "edge" {
			executable = `"${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"`
		}
		script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$start = [Diagnostics.ProcessStartInfo]::new(%s)
$start.ArgumentList.Add('%s')
$start.ArgumentList.Add("--user-data-dir=$env:TEMP\%s")
$start.ArgumentList.Add('--no-first-run')
$start.ArgumentList.Add('--no-default-browser-check')
`, executable, debug, profile)
		if url != "" {
			script += fmt.Sprintf(`$start.ArgumentList.Add('--')
$start.ArgumentList.Add([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s')))
`, base64.StdEncoding.EncodeToString([]byte(url)))
		}
		return script + "$null = [Diagnostics.Process]::Start($start)"
	}
	urlArg := ""
	if url != "" {
		urlArg = " -- " + shellQuote(url)
	}
	if osName == "macos" {
		return fmt.Sprintf(`temp=$(getconf DARWIN_USER_TEMP_DIR) && open -na 'Google Chrome' --args %s --user-data-dir="${temp%%/}/%s" --no-first-run --no-default-browser-check%s`, debug, profile, urlArg)
	}
	return fmt.Sprintf(`nohup google-chrome %s --user-data-dir="${TMPDIR:-/tmp}/%s" --no-first-run --no-default-browser-check%s </dev/null >/dev/null 2>&1 &`, debug, profile, urlArg)
}

func (e *env) vmBrowserStart(name, browser string, port int, url string) error {
	info, err := e.loadVM(name)
	if err != nil {
		return err
	}
	o, _ := e.osOfVM(name)
	remote, err := guiRemote(o, browserStartCommand(o.name, browser, port, url), time.Minute)
	if err != nil {
		return err
	}
	login := info.login()
	step("starting %s in %s", browser, name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute+30*time.Second)
	defer cancel()
	if _, err := login.runRemote(ctx, nil, remote, "vm browser start"); err != nil {
		return err
	}
	endpoint, err := waitCDP(o, login, port, browser)
	if err != nil {
		return fmt.Errorf("%s's debugging port %d in %s did not answer within a minute; pick another -port if an app already uses it: %w", browser, port, name, err)
	}
	fmt.Println(endpoint)
	return nil
}

func waitCDP(o osDef, login sshTarget, port int, app string) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	probe := fmt.Sprintf(o.guest.cdpVersion, url+"/json/version")
	wait, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	for {
		body, err := login.runContext(wait, nil, probe)
		if err == nil {
			if cdpVersionMatches(body, app) {
				return url, nil
			}
			err = fmt.Errorf("/json/version did not identify the expected app")
		}
		select {
		case <-wait.Done():
			return "", err
		case <-time.After(2 * time.Second):
		}
	}
}

func cdpVersionMatches(body, app string) bool {
	var version struct {
		Browser      string
		UserAgent    string `json:"User-Agent"`
		WebSocketURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal([]byte(body), &version); err != nil || version.WebSocketURL == "" {
		return false
	}
	isT3 := strings.Contains(version.UserAgent, t3UserAgent) || strings.Contains(version.Browser, t3UserAgent)
	switch app {
	case t3UserAgent:
		return isT3
	case "chrome":
		return !isT3 && strings.HasPrefix(version.Browser, "Chrome/")
	case "edge":
		return !isT3 && strings.HasPrefix(version.Browser, "Edg/")
	default:
		return false
	}
}
