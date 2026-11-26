//go:build !darwin && !linux && !windows

package az

import (
	"context"
	"os"
)

const routeHostsCRLF = false

func routeHostsPath() string                                      { return "/etc/hosts" }
func routeSystemOwnedByUser(os.FileInfo) error                    { return errRouteSystemUnsupported }
func routeSystemLogHint(routeSystemPlan) string                   { return "" }
func routeSystemInstallOS(context.Context, routeSystemPlan) error { return errRouteSystemUnsupported }
func routeSystemRestoreOS(context.Context, routeSystemPlan) error { return errRouteSystemUnsupported }
