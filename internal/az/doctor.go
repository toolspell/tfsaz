package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type doctorReport struct {
	Environment     string        `json:"environment"`
	Directory       string        `json:"directory"`
	Config          string        `json:"config"`
	SelectedProfile string        `json:"selectedProfile,omitempty"`
	Checks          []doctorCheck `json:"checks"`
}

func (a app) runDoctor(ctx context.Context, args []string) error {
	asJSON := false
	path := ""
	for _, arg := range args {
		switch {
		case arg == "--json" && !asJSON:
			asJSON = true
		case arg != "" && !strings.HasPrefix(arg, "-") && path == "":
			path = arg
		default:
			return usageError{errors.New("expected doctor [--json] [PATH]")}
		}
	}
	dir := path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(a.cwd, dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	report := doctorReport{Environment: "current process", Directory: dir}
	add := func(name, status, message string) {
		report.Checks = append(report.Checks, doctorCheck{name, status, message})
	}
	for _, name := range shimNames {
		status, message := checkShimOnPath(name)
		add(name, status, message)
	}
	report.Config, err = configPath()
	var cfg config
	if err == nil {
		cfg, err = loadConfig()
	}
	if err != nil {
		add("config", "error", err.Error())
	} else {
		add("config", "ok", fmt.Sprintf("%d configured profile(s)", len(cfg.Profiles)))
	}
	records, regErr := readSetupRecords()
	if regErr == nil && err == nil {
		regErr = cfg.addSetupAliases(records)
	}
	if regErr != nil {
		add("registry", "error", regErr.Error())
	}
	checkout, checkoutErr := findSetupCheckout(ctx, dir)
	switch {
	case checkoutErr != nil:
		status := "skip"
		if path != "" || ctx.Err() != nil {
			status = "error"
		}
		if _, gitErr := exec.LookPath("git"); gitErr != nil {
			status = "error"
		}
		add("checkout", status, checkoutErr.Error())
	case err != nil || regErr != nil:
		add("checkout", "skip", "profile configuration or repository registry is unavailable")
	default:
		p, _ := cfg.checkout(ctx, dir)
		if p != nil {
			report.SelectedProfile = p.ID
			add("checkout", "ok", "Git fetch remote selects profile "+p.ID)
		} else {
			status := "skip"
			if path != "" {
				status = "error"
			}
			add("checkout", status, "Git fetch remote does not identify a configured collection")
		}
		if idx := setupRecordIndex(records, checkout.gitDir); idx >= 0 {
			if verifyErr := verifySetup(ctx, a.store, checkout, records[idx], cfg); verifyErr != nil {
				add("repo verify", "error", verifyErr.Error())
			} else {
				add("repo verify", "ok", "managed Git configuration and read access verified")
			}
		}
	}
	// Outside a repository the report can check every configured profile. An
	// explicit or detected checkout must never silently turn into a global probe.
	profiles := cfg.Profiles
	if report.SelectedProfile == "" && (path != "" || checkoutErr == nil || regErr != nil) {
		profiles = nil
		add("authentication", "skip", "no unambiguous checkout profile to authenticate")
	}
	for _, p := range profiles {
		if ctx.Err() != nil {
			add("authentication", "error", ctx.Err().Error())
			break
		}
		if report.SelectedProfile != "" && report.SelectedProfile != p.ID {
			continue
		}
		client, authErr := a.client(p)
		if authErr == nil {
			probe, end := context.WithTimeout(ctx, 15*time.Second)
			_, authErr = client.Identity(probe)
			end()
			client.http.CloseIdleConnections()
		}
		name := "authentication " + p.ID
		if authErr != nil {
			add(name, "error", authErr.Error())
		} else {
			add(name, "ok", "collection verified the saved PAT")
		}
	}
	if asJSON {
		if err := json.NewEncoder(a.stdout).Encode(report); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(a.stdout, "Environment: %s\nDirectory: %s\nConfig: %s\n", report.Environment, report.Directory, report.Config); err != nil {
			return err
		}
		for _, c := range report.Checks {
			if _, err := fmt.Fprintf(a.stdout, "%s\t%s\t%s\n", c.Status, c.Name, c.Message); err != nil {
				return err
			}
		}
	}
	for _, c := range report.Checks {
		if c.Status == "error" {
			return errors.New("one or more doctor checks failed")
		}
	}
	return nil
}

func checkShimOnPath(name string) (status, message string) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "error", name + " is unavailable on this PATH; run tfsaz shim install --dir DIR and put DIR on PATH"
	}
	if binary, err := os.Executable(); err == nil && sameBinary(path, binary) {
		return "ok", path
	}
	message = path + " differs from this TFSaz executable; check PATH order and installed version"
	// The Windows fallback copies the binary, so an upgrade leaves the old copy.
	if info, err := os.Lstat(path); err == nil && runtime.GOOS == "windows" && info.Mode().IsRegular() {
		message += ". If it is a copy from an older TFSaz, delete it and rerun tfsaz shim install --dir DIR"
	}
	return "error", message
}
