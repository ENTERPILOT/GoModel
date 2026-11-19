package run

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
)

// openDashboard opens the dashboard for --open. A browser that cannot be
// opened is not a reason to stop a gateway that is otherwise starting fine:
// the failure is logged with the address to open by hand.
func openDashboard(target string) {
	if target == "" {
		slog.Warn("--open ignored: this gateway does not serve the admin dashboard")
		return
	}
	if err := launchBrowser(target); err != nil {
		slog.Warn("could not open the dashboard in a browser; open it manually", "url", target, "error", err)
		return
	}
	slog.Info("opening the dashboard in the default browser", "url", target)
}

// launchBrowser asks the operating system to open target in the user's
// default browser. It is a variable so tests can observe the request without
// opening anything.
var launchBrowser = func(target string) error {
	argv, err := browserCommand(runtime.GOOS, os.Getenv, target)
	if err != nil {
		return err
	}
	//nolint:gosec // A fixed launcher per OS; target is the dashboard URL built from the server configuration.
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Some launchers stay in the foreground until the browser exits, so the
	// gateway does not wait for them before it serves.
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Warn("the browser launcher failed; open the dashboard manually", "url", target, "error", err)
		}
	}()
	return nil
}

// browserCommand returns the command that opens target with the default
// browser on goos.
func browserCommand(goos string, getenv func(string) string, target string) ([]string, error) {
	switch goos {
	case "darwin":
		return []string{"open", target}, nil
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", target}, nil
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		// Without a graphical session xdg-open falls back to a terminal
		// browser, which has no terminal to draw on here.
		if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
			return nil, errors.New("no graphical session (DISPLAY and WAYLAND_DISPLAY are unset)")
		}
		return []string{"xdg-open", target}, nil
	default:
		return nil, fmt.Errorf("opening a browser is not supported on %s", goos)
	}
}
