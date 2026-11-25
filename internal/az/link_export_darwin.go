package az

import "context"

const desktopHandlerExample = `route handler-export "$HOME/Applications/tfsaz Links.app" com.apple.Safari`

func exportDesktopLinkHandler(ctx context.Context, dest, browser string) error {
	return exportMacLinkHandler(ctx, dest, browser)
}
