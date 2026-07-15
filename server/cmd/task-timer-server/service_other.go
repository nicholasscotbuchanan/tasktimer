//go:build !windows

package main

import "errors"

// runService reports whether the process was started by a service manager and,
// if so, the outcome of running under it. Only Windows has a Service Control
// Manager this program hooks into; everywhere else this is a no-op so main()
// takes the foreground path (started by systemd/launchd as an ordinary process
// that stops on SIGTERM).
func runService() (handled bool, err error) { return false, nil }

// serviceControl backs the `service` subcommand, which manages the Windows
// service. There is nothing to manage on other platforms.
func serviceControl(args []string) error {
	return errors.New("the 'service' command is only supported on Windows; " +
		"on Linux use systemctl, on macOS use launchctl")
}
