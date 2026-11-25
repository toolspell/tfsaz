package az

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

type traceKey struct{}

type traceSelection string

const (
	traceOrganization traceSelection = "organization"
	traceCheckout     traceSelection = "checkout"
	traceGitRemote    traceSelection = "git-remote"
	traceDiscovery    traceSelection = "discovery"
	traceRESTURL      traceSelection = "rest-url"
)

type commandTrace struct {
	stderr    io.Writer
	command   string
	profile   string
	selection traceSelection
	start     time.Time
	recorded  bool
}

func startCommandTrace(ctx context.Context, stderr io.Writer, command string) (context.Context, *commandTrace) {
	if os.Getenv("TFSAZ_TRACE") != "1" || stderr == nil {
		return ctx, nil
	}
	trace := &commandTrace{stderr: stderr, command: command, profile: "-", selection: "none", start: time.Now()}
	return context.WithValue(ctx, traceKey{}, trace), trace
}

func getCommandTrace(ctx context.Context) *commandTrace {
	trace, _ := ctx.Value(traceKey{}).(*commandTrace)
	return trace
}

func (t *commandTrace) selectProfile(p Profile, why traceSelection) {
	if t == nil {
		return
	}
	if t.selection != "none" {
		t.finish()
	}
	if profileID.MatchString(p.ID) {
		t.profile = p.ID
	} else {
		t.profile = "-"
	}
	t.selection, t.start, t.recorded = why, time.Now(), false
}

func (t *commandTrace) finish() {
	if t != nil && !t.recorded {
		t.record(0, t.start)
	}
}

func (t *commandTrace) record(code int, start time.Time) {
	status := "none"
	if code != 0 {
		status = strconv.Itoa(code)
	}
	// Emit only known command and selection names, a validated ID, and
	// numeric HTTP metadata. Never format a request, response, or error here.
	fmt.Fprintf(t.stderr, "tfsaz trace command=%q profile=%s selection=%s status=%s elapsed=%s\n",
		t.command, t.profile, t.selection, status, time.Since(start).Round(time.Microsecond))
	t.recorded = true
}
