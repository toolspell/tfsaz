package harness

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	liveEnv      = "TFSAZ_LIVE"
	osEnv        = "TFSAZ_LIVE_OS"
	keepEnv      = "TFSAZ_LIVE_KEEP"
	artifactsEnv = "TFSAZ_LIVE_ARTIFACTS"
)

type lookupEnv func(string) (string, bool)

type config struct {
	platforms []string
	limits    map[string]int
	keep      bool
	artifacts string
}

func skipReason(lookup lookupEnv) string {
	for _, name := range []string{"CI", "GITHUB_ACTIONS"} {
		if _, set := lookup(name); set {
			return "live tests are disabled when " + name + " is set"
		}
	}
	if value, _ := lookup(liveEnv); value != "1" {
		return "set " + liveEnv + "=1 to run live tests"
	}
	return ""
}

func readConfig(lookup lookupEnv, root string) (config, error) {
	cfg := config{
		platforms: []string{"ubuntu", "macos", "windows"},
		limits:    map[string]int{"ubuntu": 2, "macos": 2, "windows": 1},
	}
	if list, _ := lookup(osEnv); list != "" {
		cfg.platforms = nil
		for osName := range strings.SplitSeq(list, ",") {
			osName = strings.TrimSpace(osName)
			if _, ok := cfg.limits[osName]; !ok {
				return config{}, fmt.Errorf("%s has unknown OS %q (use ubuntu, macos or windows)", osEnv, osName)
			}
			if !slices.Contains(cfg.platforms, osName) {
				cfg.platforms = append(cfg.platforms, osName)
			}
		}
	}
	for _, osName := range cfg.platforms {
		name := "TFSAZ_LIVE_MAX_" + strings.ToUpper(osName)
		if value, _ := lookup(name); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return config{}, fmt.Errorf("%s must be a positive integer", name)
			}
			if osName == "macos" && n > 2 {
				return config{}, fmt.Errorf("%s cannot exceed 2", name)
			}
			cfg.limits[osName] = n
		}
	}
	keep, _ := lookup(keepEnv)
	cfg.keep = keep == "1"
	cfg.artifacts, _ = lookup(artifactsEnv)
	if cfg.artifacts != "" && !filepath.IsAbs(cfg.artifacts) {
		cfg.artifacts = filepath.Join(root, cfg.artifacts)
	}
	return cfg, nil
}

type semaphore chan struct{}

func (s semaphore) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s semaphore) release() { <-s }
