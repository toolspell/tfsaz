package az

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const desktopHandlerExample = `route handler-export "$HOME/.local/share/applications/com.toolspell.tfsaz.links.desktop" /usr/bin/firefox`

func exportDesktopLinkHandler(ctx context.Context, dest, browserExecutable string) (err error) {
	if !validLinkPath(dest) || filepath.Ext(dest) != ".desktop" || filepath.Base(dest) == ".desktop" {
		return errors.New("specify a new .desktop file path")
	}
	executable, config, err := linkHandlerPaths(browserExecutable)
	if err != nil {
		return err
	}
	// The desktop entry spec disallows '=' in the executable path. GIO
	// checks the executable before unescaping literal percent signs.
	if strings.ContainsAny(executable, "=%") {
		return errors.New("the TFSaz executable path cannot contain = or % in a desktop entry")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolve link handler destination: %w", err)
	}
	stage, err := os.CreateTemp(filepath.Dir(dest), ".tfsaz-link-handler-*")
	if err != nil {
		return fmt.Errorf("stage link handler: %w", err)
	}
	defer removeStaged(stage.Name(), &err)
	_, writeErr := stage.WriteString(desktopLinkHandlerEntry(executable, config, browserExecutable))
	if err := errors.Join(writeErr, stage.Close()); err != nil {
		return fmt.Errorf("write link handler: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(stage.Name(), dest); err != nil {
		return fmt.Errorf("install link handler: %w", err)
	}
	return nil
}

func desktopLinkHandlerEntry(executable, config, browser string) string {
	// Desktop entries decode value escapes before parsing Exec argument
	// quoting. Leave %u as the sole URL field code and escape literal percents.
	escape := strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", `$`, `\\$`, `%`, `%%`)
	quote := func(path string) string { return `"` + escape.Replace(path) + `"` }
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=tfsaz Links
Exec=%s route handler-open %s %s %%u
Terminal=false
Categories=Network;WebBrowser;
MimeType=x-scheme-handler/http;x-scheme-handler/https;
`, quote(executable), quote(config), quote(browser))
}
