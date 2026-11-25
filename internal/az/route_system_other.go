//go:build !darwin && !linux && !windows

package az

import (
	"context"
	"errors"
	"os"
)

const routeSystemSystemTrust = true

const routeHostsCRLF = false

var errRouteSystemUnsupported = errors.New("browser routing setup supports macOS, Linux and Windows only")

func routeHostsPath() string                                      { return "/etc/hosts" }
func routeSystemOwnedByUser(os.FileInfo) error                    { return errRouteSystemUnsupported }
func routeSystemLogHint(routeSystemPlan) string                   { return "" }
func routeSystemInstallOS(context.Context, routeSystemPlan) error { return errRouteSystemUnsupported }
func routeSystemRestoreOS(context.Context, routeSystemPlan) error { return errRouteSystemUnsupported }
