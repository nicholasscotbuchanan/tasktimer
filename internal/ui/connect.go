package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	tsync "task-timer-app/internal/sync"
)

// connectTimeout bounds the whole browser round trip. The gateway's own flow
// caps itself too; this is the app's backstop so a user who wandered off does
// not leave a spinner up for the rest of the day.
const connectTimeout = 6 * time.Minute

// Synchronize is the Synchronize button's entry point.
//
// A sync that would just bounce off a 401 helps nobody, so before queueing
// anything the app makes sure the machine is actually signed in to its backend.
// When a connectable provider is enabled (or nothing is configured yet) and no
// credential exists, it offers sign-in first — enter the URL, click Log in — and
// queues the sessions once that succeeds. Otherwise it queues straight away, so
// a file- or token-only setup is untouched.
func (a *App) Synchronize() {
	if reg, ok := a.needsConnect(); ok {
		a.connectDialog(reg, a.queueSync)
		return
	}
	a.queueSync()
}

// needsConnect returns the connectable provider the user should sign in to
// before syncing, if any.
//
// It offers sign-in when a connectable provider is enabled but has no token, or
// when nothing at all is configured yet — the fresh-install case, where the
// point is to get the user connected. A provider the user has deliberately left
// disabled in favour of another backend is never forced on them.
func (a *App) needsConnect() (tsync.Registration, bool) {
	cfg, err := tsync.LoadConfig(tsync.ConfigPath())
	if err != nil {
		// A broken config is the user's to fix by hand; do not paper over it with
		// a sign-in prompt that would then save over it.
		return tsync.Registration{}, false
	}

	enabled := map[string]bool{}
	anyEnabled := false
	for _, pc := range cfg.Providers {
		enabled[pc.Name] = pc.Enabled
		if pc.Enabled {
			anyEnabled = true
		}
	}

	for _, reg := range tsync.Connectable() {
		if reg.HasToken != nil && reg.HasToken() {
			continue
		}
		if enabled[reg.Name] || !anyEnabled {
			return reg, true
		}
	}
	return tsync.Registration{}, false
}

// connectDialog asks for the backend URL and, on Log in, runs the sign-in. The
// URL is pre-filled from the config when the provider has been pointed at one
// already, so reconnecting is a single click.
func (a *App) connectDialog(reg tsync.Registration, onConnected func()) {
	url := widget.NewEntry()
	url.SetPlaceHolder("https://tasktimer.example.com")
	if current := a.providerURL(reg); current != "" {
		url.SetText(current)
	}

	items := []*widget.FormItem{
		widget.NewFormItem("Backend URL", url),
	}

	d := dialog.NewForm("Connect to "+reg.Title, "Log in", "Cancel", items,
		func(ok bool) {
			if !ok {
				return
			}
			target := strings.TrimSpace(url.Text)
			if target == "" {
				a.reportError("Connecting", errors.New("enter the backend URL first"))
				return
			}
			a.runConnect(reg, target, onConnected)
		}, a.window)
	d.Resize(fyne.NewSize(460, 0))
	d.Show()
}

// runConnect drives the provider's sign-in off the UI goroutine — it opens a
// browser and blocks until the user consents — then persists the URL, enables
// the provider, and hands back to onConnected. A "waiting for the browser"
// notice stands in until it finishes.
func (a *App) runConnect(reg tsync.Registration, url string, onConnected func()) {
	waiting := dialog.NewCustomWithoutButtons("Connecting to "+reg.Title,
		container.NewVBox(
			widget.NewLabel("A browser window has opened for sign-in."),
			widget.NewLabel("Finish there, then come back — this closes on its own."),
		), a.window)
	waiting.Show()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()

		id, err := tsync.Connect(ctx, reg.Name, url)
		waiting.Hide()
		if err != nil {
			a.reportError("Connecting to "+reg.Title, err)
			return
		}

		if err := a.saveConnection(reg, url); err != nil {
			a.reportError("Saving the connection", err)
			return
		}

		who := id.DisplayName
		if who == "" {
			who = id.Email
		}
		message := "This machine is now connected."
		if who != "" {
			message = "Connected as " + who + "."
		}
		if id.SiteURL != "" {
			message += "\nTracker site: " + id.SiteURL
		}
		dialog.ShowInformation("Connected to "+reg.Title, message, a.window)

		a.reload()
		if onConnected != nil {
			onConnected()
		}
	}()
}

// saveConnection records a successful sign-in in the daemon's config: it writes
// the URL into the provider's settings and switches the provider on, so the next
// sync cycle actually reaches the backend the user just authorised.
//
// Like the Settings page, it reloads before writing so keys the app does not
// render — an inline token, a hand-added field — survive the round trip.
func (a *App) saveConnection(reg tsync.Registration, url string) error {
	cfg, err := tsync.LoadConfig(tsync.ConfigPath())
	if err != nil {
		return err
	}

	index := -1
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == reg.Name {
			index = i
			break
		}
	}
	if index < 0 {
		cfg.Providers = append(cfg.Providers, tsync.ProviderConfig{
			Name:     reg.Name,
			Settings: tsync.DefaultSettings(reg.Name),
		})
		index = len(cfg.Providers) - 1
	}

	if reg.URLField != "" {
		settings := decodeSettings(cfg.Providers[index].Settings)
		raw, err := json.Marshal(url)
		if err != nil {
			return err
		}
		settings[reg.URLField] = raw

		encoded, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		cfg.Providers[index].Settings = encoded
	}

	cfg.Providers[index].Enabled = true
	return tsync.SaveConfig(tsync.ConfigPath(), cfg)
}

// providerURL reads the backend URL a provider is currently pointed at, so the
// Connect dialog can pre-fill it. It returns "" when none is set.
func (a *App) providerURL(reg tsync.Registration) string {
	if reg.URLField == "" {
		return ""
	}
	cfg, err := tsync.LoadConfig(tsync.ConfigPath())
	if err != nil {
		return ""
	}
	for _, pc := range cfg.Providers {
		if pc.Name != reg.Name {
			continue
		}
		settings := decodeSettings(pc.Settings)
		raw, ok := settings[reg.URLField]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
	}
	return ""
}

// plural renders "1 session" / "3 sessions".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
