//go:build tray && darwin

// This file is compiled ONLY into the macOS .app build (go build -tags tray,
// CGO_ENABLED=1). It puts a menu bar (status bar) item on screen showing the
// address where the gateway is reached and configured, so an operator running
// the headless server on a Mac can see at a glance where to point a browser or
// the desktop clients.
//
// It depends on AppKit through fyne.io/systray, which is CGO - the one build
// where the otherwise pure-Go server is not. Every other target (the .deb, .rpm,
// Docker and Windows builds) excludes this file and stays headless and CGO-free.

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"os/signal"
	"syscall"

	"fyne.io/systray"

	"task-timer-server/internal/config"
)

// runTray runs the gateway in the background and a menu bar item in the
// foreground. systray.Run must own the main goroutine on macOS, so main() calls
// this directly rather than in a goroutine.
//
// A failure to start the server does NOT tear the tray down: the whole reason
// the tray exists is to show WHERE to configure the gateway, which matters most
// precisely when it is not configured yet. The tray lives until the user picks
// Quit, or the process receives SIGINT/SIGTERM (launchctl stop).
func runTray(run func(context.Context) error) (bool, error) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Remove the menu bar item once the process is asked to exit.
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()

	errc := make(chan error, 1)
	go func() { errc <- run(ctx) }()

	systray.Run(func() { buildMenu(stop) }, func() {})

	stop()
	if err := <-errc; err != nil {
		log.Printf("task-timer-server: %v", err)
	}
	return true, nil
}

func buildMenu(stop context.CancelFunc) {
	systray.SetTitle("Task Timer")
	systray.SetTooltip("Task Timer gateway")

	url := configureURL()

	header := systray.AddMenuItem("Configure Task Timer at:", "")
	header.Disable()
	addr := systray.AddMenuItem(url, "Open this address to configure Task Timer")
	systray.AddSeparator()
	openItem := systray.AddMenuItem("Open in Browser", "Open the address in your default browser")
	copyItem := systray.AddMenuItem("Copy Address", "Copy the address to the clipboard")
	systray.AddSeparator()
	quitItem := systray.AddMenuItem("Quit Task Timer Server", "Stop the gateway and quit")

	go func() {
		for {
			select {
			case <-addr.ClickedCh:
				openURL(url)
			case <-openItem.ClickedCh:
				openURL(url)
			case <-copyItem.ClickedCh:
				pbcopy(url)
			case <-quitItem.ClickedCh:
				stop()
				return
			}
		}
	}()
}

// configureURL is the address to show: the gateway's listen port from its config
// (default 8080), on the machine's primary LAN IP so it is the address another
// device would actually use. A loopback-bound server is shown as 127.0.0.1,
// because that is genuinely all it is reachable on.
func configureURL() string {
	port := 8080
	host := ""
	if cfg, err := config.Load(); err == nil {
		if cfg.Port != 0 {
			port = cfg.Port
		}
		host = cfg.Host
	}
	return fmt.Sprintf("http://%s:%d", displayIP(host), port)
}

func displayIP(host string) string {
	switch host {
	case "", "0.0.0.0", "::":
		return lanIP()
	case "127.0.0.1", "localhost", "::1":
		return "127.0.0.1"
	default:
		return host
	}
}

// lanIP is the machine's primary outbound IPv4. The UDP "dial" sends no packets;
// it just makes the kernel resolve which local interface would carry traffic to
// that destination, which is the address other machines on the LAN reach us on.
func lanIP() string {
	if c, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP != nil {
			return a.IP.String()
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
				if v4 := n.IP.To4(); v4 != nil {
					return v4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

func openURL(u string) { _ = exec.Command("open", u).Start() }

func pbcopy(s string) {
	cmd := exec.Command("pbcopy")
	in, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_, _ = io.WriteString(in, s)
	_ = in.Close()
	_ = cmd.Wait()
}
