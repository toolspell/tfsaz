package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestLiveLocks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"instance lock", checkInstanceLock},
		{"base locks", checkBaseLock},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkInstanceLock(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	unlock, err := e.lockInstance()
	if err != nil {
		t.Fatal(err)
	}
	path := e.path("instance-locks", "first.lock")
	other := flock.New(path)
	if ok, err := other.TryLock(); ok || err != nil {
		t.Errorf("second TryLock = %t, %v; want contention", ok, err)
		_ = other.Close()
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if release, err := takeLock(ctx, other, false); err == nil || !strings.Contains(err.Error(), "is busy") {
		t.Errorf("takeLock with expired deadline = %v; want busy error", err)
		if release != nil {
			release()
		}
	}
	unlock()
	if release, err := e.lockInstance(); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
}

func checkBaseLock(t *testing.T) {
	builder := &env{home: t.TempDir(), instance: "builder"}
	other := &env{home: builder.home, instance: "other"}
	for _, base := range oses {
		t.Run(base.name, func(t *testing.T) {
			unlock, err := builder.lockBase(base, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(unlock)
			for _, guest := range oses {
				path := other.path("base-locks", guest.name+".lock")
				for _, shared := range []bool{false, true} {
					probe := flock.New(path)
					tryLock := probe.TryLock
					if shared {
						tryLock = probe.TryRLock
					}
					got, err := tryLock()
					_ = probe.Close()
					if want := guest.name != base.name; got != want || err != nil {
						t.Errorf("lock %s (shared=%t) = %t, %v; want %t", path, shared, got, err, want)
					}
				}
			}
		})
	}
}

func putLiveFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureLiveStderr(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = file
	t.Cleanup(func() {
		os.Stderr = old
		_ = file.Close()
	})
	return file.Name()
}

func readLiveFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
