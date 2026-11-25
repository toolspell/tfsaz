//go:build !darwin

package az

import (
	"context"
	"errors"
	"io"
)

const routeInheritedSocketFlag = "--launchd-socket"

func serveRouteRedirectInherited(context.Context, string, string, int, io.Writer) error {
	return errors.New("an inherited launchd socket is available only on macOS")
}
