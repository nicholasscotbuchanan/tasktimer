//go:build !(tray && darwin)

package main

import "context"

// runTray reports whether a menu bar (tray) UI took over the process. The tray
// exists only in the macOS .app build (built with -tags tray and CGO); every
// other build - the .deb, .rpm, Docker and Windows binaries - is headless and
// pure Go, so this returns false and main() runs the server directly.
func runTray(run func(context.Context) error) (bool, error) { return false, nil }
