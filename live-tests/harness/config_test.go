package harness

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReadConfig(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	all := []string{"ubuntu", "macos", "windows"}
	defaults := map[string]int{"ubuntu": 2, "macos": 2, "windows": 1}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want config
		err  string
	}{
		{name: "defaults", want: config{platforms: all, limits: defaults}},
		{
			name: "selected guests and overrides",
			env:  map[string]string{osEnv: "windows, ubuntu,windows", keepEnv: "1", artifactsEnv: "results", "TFSAZ_LIVE_MAX_WINDOWS": "3"},
			want: config{platforms: []string{"windows", "ubuntu"}, limits: map[string]int{"ubuntu": 2, "macos": 2, "windows": 3}, keep: true, artifacts: filepath.Join(root, "results")},
		},
		{name: "absolute artifacts", env: map[string]string{artifactsEnv: home, keepEnv: "true"}, want: config{platforms: all, limits: defaults, artifacts: home}},
		{name: "unknown guest", env: map[string]string{osEnv: "linux"}, err: osEnv},
		{name: "empty guest in list", env: map[string]string{osEnv: "ubuntu,"}, err: osEnv},
		{name: "invalid limit", env: map[string]string{"TFSAZ_LIVE_MAX_UBUNTU": "many"}, err: "positive integer"},
		{name: "zero limit", env: map[string]string{"TFSAZ_LIVE_MAX_WINDOWS": "0"}, err: "positive integer"},
		{name: "macOS cap", env: map[string]string{"TFSAZ_LIVE_MAX_MACOS": "3"}, err: "cannot exceed 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readConfig(envLookup(tc.env), root)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("readConfig error = %v; want %q", err, tc.err)
				}
				return
			}
			if err != nil || !slices.Equal(got.platforms, tc.want.platforms) || !maps.Equal(got.limits, tc.want.limits) ||
				got.keep != tc.want.keep || got.artifacts != tc.want.artifacts {
				t.Fatalf("readConfig = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestSkipReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unset", nil, liveEnv + "=1"},
		{"requires 1", map[string]string{liveEnv: "true"}, liveEnv + "=1"},
		{"opt-in", map[string]string{liveEnv: "1"}, ""},
		{"CI with opt-in", map[string]string{liveEnv: "1", "CI": "true"}, "CI is set"},
		{"GitHub Actions empty", map[string]string{liveEnv: "1", "GITHUB_ACTIONS": ""}, "GITHUB_ACTIONS is set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := skipReason(envLookup(tc.env))
			if (tc.want == "" && got != "") || !strings.Contains(got, tc.want) {
				t.Fatalf("skipReason = %q; want %q", got, tc.want)
			}
		})
	}
}

func envLookup(env map[string]string) lookupEnv {
	return func(name string) (string, bool) {
		value, set := env[name]
		return value, set
	}
}

func TestSemaphore(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	slot := make(semaphore, 2)
	otherOS := make(semaphore, 1)
	for range 2 {
		if err := slot.acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := otherOS.acquire(ctx); err != nil {
		t.Fatalf("other OS has its own slot: %v", err)
	}
	otherOS.release()

	held, stop := context.WithCancel(ctx)
	stop()
	if err := slot.acquire(held); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire with canceled context = %v", err)
	}

	started, acquired := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		acquired <- slot.acquire(ctx)
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-acquired:
		t.Fatalf("acquired a third slot while two are held: %v", err)
	default:
	}
	slot.release()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	slot.release()
	slot.release()
}
