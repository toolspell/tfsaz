//go:build !darwin && !linux && !windows

package az

import (
	"context"
	"errors"
)

const desktopHandlerExample = `route handler-export DESTINATION BROWSER`

func exportDesktopLinkHandler(context.Context, string, string) error {
	return errors.New("desktop link handler export requires macOS, Linux or Windows")
}
