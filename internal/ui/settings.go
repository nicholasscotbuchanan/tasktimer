package ui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	tsync "task-timer-app/internal/sync"
	"task-timer-app/internal/task"
)

// settingsPage edits two different things that happen to live side by side.
//
// The working day is an app preference: it only affects what this window counts
// down from, so it goes in Fyne's preference store. Everything else is the sync
// daemon's config file, which the daemon reads and this app only writes — so the
// page loads it fresh on every visit rather than caching a copy that could go
// stale behind a hand-edit.
//
// Crucially, this page knows nothing about any particular backend. It
// walks the provider registry and renders a form from each provider's declared
// Fields. That is what keeps the plugin contract honest: a new backend is one
// file that implements Provider and calls Register, and its settings appear here
// with nothing in the app changed. Importing a provider package here — which an
// earlier version of this file did — quietly turns a plugin system back into a
// hard-coded one.
type settingsPage struct {
	app     *App
	content fyne.CanvasObject

	workingHours *widget.Entry
	pollInterval *widget.Entry

	providers []*providerForm

	status *fyne.Container
}

// providerForm is the rendered settings block for one registered provider.
type providerForm struct {
	reg     tsync.Registration
	enabled *widget.Check

	// controls holds one widget per declared field, keyed by the field's JSON
	// key. The value is a *widget.Entry or a *widget.Check, matching the Kind.
	controls map[string]fyne.CanvasObject
}

func newSettingsPage(a *App) *settingsPage {
	p := &settingsPage{app: a, status: container.NewHBox()}

	p.workingHours = widget.NewEntry()
	p.workingHours.SetPlaceHolder("8")

	p.pollInterval = widget.NewEntry()
	p.pollInterval.SetPlaceHolder(tsync.DefaultPollIntervalText)

	save := iconButton("Save changes", iconCheck, true, p.save)
	discard := iconButton("Discard", iconReset, false, p.refresh)

	actions := container.NewBorder(nil, nil,
		centreY(p.status),
		container.NewHBox(discard, save),
	)

	cards := []fyne.CanvasObject{
		card("Timekeeping", p.timekeepingForm()),
		insetXY(card("Sync Daemon", p.syncForm()), 0, 14),
	}

	// One card per compiled-in provider, built from what the provider says about
	// itself. A build with no providers linked in simply shows no provider cards.
	for _, form := range p.buildProviderForms() {
		cards = append(cards, insetXY(card(form.reg.Title, p.providerForm(form)), 0, 14))
	}

	cards = append(cards,
		card("Data", p.dataForm()),
		insetXY(actions, 0, 14),
	)

	body := container.NewVBox(cards...)

	// The right inset is the scrollbar's lane, so it is not drawn over the cards.
	p.content = container.NewVScroll(container.New(insetLayout{right: 10}, body))
	return p
}

// buildProviderForms creates a form for every registered provider, once.
func (p *settingsPage) buildProviderForms() []*providerForm {
	if p.providers != nil {
		return p.providers
	}

	for _, reg := range tsync.Descriptors() {
		form := &providerForm{
			reg:      reg,
			enabled:  widget.NewCheck("Enabled", nil),
			controls: make(map[string]fyne.CanvasObject, len(reg.Fields)),
		}

		for _, field := range reg.Fields {
			switch field.Kind {
			case tsync.KindBool:
				form.controls[field.Key] = widget.NewCheck("", nil)
			default:
				entry := widget.NewEntry()
				entry.SetPlaceHolder(field.Placeholder)
				form.controls[field.Key] = entry
			}
		}

		p.providers = append(p.providers, form)
	}
	return p.providers
}

// ---------------------------------------------------------------------------
// Forms
// ---------------------------------------------------------------------------

func (p *settingsPage) timekeepingForm() fyne.CanvasObject {
	return formGrid(
		formRow("Working day (hours)",
			"What the footer and the Reports target count down from.",
			p.workingHours),
	)
}

func (p *settingsPage) syncForm() fyne.CanvasObject {
	return formGrid(
		formRow("Poll interval",
			"How often the daemon runs a sync cycle. A Go duration, e.g. 60s or 5m.",
			p.pollInterval),
	)
}

// providerForm renders one provider's card: the enable toggle, then a row per
// field it declared.
func (p *settingsPage) providerForm(form *providerForm) fyne.CanvasObject {
	rows := make([]fyne.CanvasObject, 0, len(form.reg.Fields)+1)

	hint := form.reg.Summary
	if hint == "" {
		hint = "The daemon skips disabled providers entirely."
	}
	rows = append(rows, formRow("Status", hint, form.enabled))

	for _, field := range form.reg.Fields {
		rows = append(rows, formRow(field.Label, field.Hint, form.controls[field.Key]))
	}

	// A provider with an interactive sign-in gets a Log in button that connects
	// using the URL in its own form, so the whole setup — point at the backend,
	// authorise, enable — happens on this one card.
	if form.reg.Connectable() {
		rows = append(rows, formRow("Sign in",
			"Authorise this machine in a browser; the token is stored for the daemon.",
			p.connectButton(form)))
	}

	return formGrid(rows...)
}

// connectButton runs the provider's sign-in against whatever URL its form
// currently shows, falling back to the saved one. It reuses the same flow the
// Synchronize button triggers.
func (p *settingsPage) connectButton(form *providerForm) fyne.CanvasObject {
	return iconButton("Log in", iconExternal, true, func() {
		url := ""
		if entry, ok := form.controls[form.reg.URLField].(*widget.Entry); ok {
			url = strings.TrimSpace(entry.Text)
		}
		if url == "" {
			url = p.app.providerURL(form.reg)
		}
		if url == "" {
			p.setStatus("Enter the "+form.reg.Title+" URL, then Log in.", colDanger)
			return
		}
		p.app.runConnect(form.reg, url, p.refresh)
	})
}

func (p *settingsPage) dataForm() fyne.CanvasObject {
	return formGrid(
		formRow("Database", "", pathValue(task.DBPath())),
		formRow("Sync config", "", pathValue(tsync.ConfigPath())),
	)
}

// ---------------------------------------------------------------------------
// Load and save
// ---------------------------------------------------------------------------

// refresh reloads the form from the preference store and the config file. It is
// also the Discard button: re-reading from disk is exactly what discarding
// unsaved edits means.
func (p *settingsPage) refresh() {
	hours := p.app.fyne.Preferences().FloatWithFallback(prefWorkingDayHours, defaultWorkingHours)
	p.workingHours.SetText(strconv.FormatFloat(hours, 'f', -1, 64))

	cfg, err := tsync.LoadConfig(tsync.ConfigPath())
	if err != nil {
		// A malformed config is the user's to fix by hand; showing the defaults
		// as if it had loaded would invite them to save over it.
		p.setStatus(fmt.Sprintf("Could not read the sync config: %v", err), colDanger)
		return
	}

	p.pollInterval.SetText(cfg.PollInterval)

	// Index the file's provider blocks so each form can find its own.
	blocks := make(map[string]tsync.ProviderConfig, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		blocks[provider.Name] = provider
	}

	for _, form := range p.providers {
		block, configured := blocks[form.reg.Name]
		form.enabled.SetChecked(configured && block.Enabled)

		settings := decodeSettings(block.Settings)

		for _, field := range form.reg.Fields {
			raw, present := settings[field.Key]

			switch control := form.controls[field.Key].(type) {
			case *widget.Check:
				value, _ := field.Default.(bool)
				if present {
					_ = json.Unmarshal(raw, &value)
				}
				control.SetChecked(value)

			case *widget.Entry:
				var value string
				if present {
					_ = json.Unmarshal(raw, &value)
				}
				control.SetText(value)
			}
		}
	}

	p.setStatus("", colTextMuted)
}

func (p *settingsPage) save() {
	hours, err := strconv.ParseFloat(p.workingHours.Text, 64)
	if err != nil || hours <= 0 || hours > 24 {
		p.setStatus("Working day must be a number of hours between 0 and 24.", colDanger)
		return
	}

	interval := p.pollInterval.Text
	if interval == "" {
		interval = tsync.DefaultPollIntervalText
	}
	if _, err := time.ParseDuration(interval); err != nil {
		p.setStatus(fmt.Sprintf("Poll interval %q is not a duration, e.g. 60s.", interval), colDanger)
		return
	}

	// Reload before writing so that anything this form does not render survives
	// a save: an inline api_token, a hand-added key, or a provider that is in the
	// file but not compiled into this build.
	cfg, err := tsync.LoadConfig(tsync.ConfigPath())
	if err != nil {
		p.app.reportError("Reading the sync config", err)
		return
	}
	cfg.PollInterval = interval

	for _, form := range p.providers {
		index := -1
		for i := range cfg.Providers {
			if cfg.Providers[i].Name == form.reg.Name {
				index = i
				break
			}
		}

		// A provider compiled in but absent from the file — because the build
		// gained a backend since the config was written — is appended rather
		// than ignored.
		if index < 0 {
			cfg.Providers = append(cfg.Providers, tsync.ProviderConfig{Name: form.reg.Name})
			index = len(cfg.Providers) - 1
		}

		settings := decodeSettings(cfg.Providers[index].Settings)

		for _, field := range form.reg.Fields {
			var value any

			switch control := form.controls[field.Key].(type) {
			case *widget.Check:
				value = control.Checked
			case *widget.Entry:
				value = control.Text
			default:
				continue
			}

			raw, err := json.Marshal(value)
			if err != nil {
				p.app.reportError("Encoding the "+form.reg.Title+" settings", err)
				return
			}
			settings[field.Key] = raw
		}

		encoded, err := json.Marshal(settings)
		if err != nil {
			p.app.reportError("Encoding the "+form.reg.Title+" settings", err)
			return
		}

		cfg.Providers[index].Enabled = form.enabled.Checked
		cfg.Providers[index].Settings = encoded
	}

	if err := tsync.SaveConfig(tsync.ConfigPath(), cfg); err != nil {
		p.app.reportError("Saving the sync config", err)
		return
	}

	p.app.fyne.Preferences().SetFloat(prefWorkingDayHours, hours)
	p.app.updateFooter()

	p.setStatus("Saved. The daemon picks the config up on its next cycle.", colSuccess)

	dialog.ShowInformation("Settings saved",
		"The sync daemon re-reads its config on the next poll; the working day applies immediately.",
		p.app.window)
}

// decodeSettings unpacks a provider's settings block into raw keys. Keeping the
// values as RawMessage rather than decoding them is what lets a key the form
// never renders survive a round trip untouched.
func decodeSettings(raw json.RawMessage) map[string]json.RawMessage {
	settings := map[string]json.RawMessage{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &settings)
	}
	return settings
}

// setStatus writes the inline message beside the Save button. An empty message
// clears the line rather than leaving a stale "Saved" sitting under a form the
// user has since edited.
func (p *settingsPage) setStatus(text string, c color.Color) {
	if text == "" {
		p.status.Objects = nil
		p.status.Refresh()
		return
	}

	msg := muted(text)
	msg.Color = c

	p.status.Objects = []fyne.CanvasObject{msg}
	p.status.Refresh()
}

// ---------------------------------------------------------------------------
// Form scaffolding
// ---------------------------------------------------------------------------

// formGrid stacks rows with a divider between them.
func formGrid(rows ...fyne.CanvasObject) fyne.CanvasObject {
	out := make([]fyne.CanvasObject, 0, len(rows)*2)
	for i, r := range rows {
		if i > 0 {
			out = append(out, insetXY(hairline(), 0, 10))
		}
		out = append(out, r)
	}
	return container.NewVBox(out...)
}

// formRow is a label and optional hint on the left, the control on the right.
func formRow(label, hint string, control fyne.CanvasObject) fyne.CanvasObject {
	name := muted(label)
	name.Color = colText

	left := container.NewVBox(name)
	if hint != "" {
		h := muted(hint)
		h.TextSize = 11
		left.Add(h)
	}

	// The control keeps a fixed width so the column of inputs lines up down the
	// page rather than each row sizing to its own content.
	right := sized(control, 360, 36)

	return container.NewBorder(nil, nil, centreY(left), centreY(right))
}

// pathValue shows a filesystem path in a form row. It is a truncating Label
// rather than the canvas text the other read-only values use, because a data
// directory is easily long enough to run off the edge of the card, and
// canvas.Text has no notion of truncation — it simply overflows.
func pathValue(path string) fyne.CanvasObject {
	l := widget.NewLabel(path)
	l.Truncation = fyne.TextTruncateEllipsis
	l.Importance = widget.LowImportance
	return centreY(l)
}
