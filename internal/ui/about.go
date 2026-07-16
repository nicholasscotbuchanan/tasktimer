package ui

import (
	"fmt"
	"runtime"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"

	"task-timer-app/internal/reconcile"
	"task-timer-app/internal/task"
)

// Build metadata, stamped by the Makefile's -ldflags. The defaults are what a
// plain `go build` or `go run .` produces, and they say so rather than claiming
// to be a release.
var (
	Version = "dev"
	Commit  = "unknown"
)

type aboutPage struct {
	app     *App
	content fyne.CanvasObject
}

func newAboutPage(a *App) *aboutPage {
	p := &aboutPage{app: a}

	logo := canvas.NewImageFromResource(appIcon())
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(72, 72))

	name := canvas.NewText("Task Timer", colText)
	name.TextSize = 22
	name.TextStyle.Bold = true

	tagline := muted("A desktop timer that logs work sessions locally and reconciles them with your task tracker.")

	identity := container.NewHBox(
		centreY(logo),
		insetXY(container.NewVBox(name, tagline), 16, 0),
	)

	body := container.NewVBox(
		card("", inset(identity, 6)),
		insetXY(card("Build", p.buildInfo()), 0, 14),
		card("Data Locations", p.locations()),
		insetXY(card("Third-Party", p.credits()), 0, 14),
	)

	p.content = container.NewVScroll(container.New(insetLayout{right: 10}, body))
	return p
}

func (p *aboutPage) buildInfo() fyne.CanvasObject {
	return formGrid(
		formRow("Version", "", centreY(muted(Version))),
		formRow("Commit", "", centreY(muted(Commit))),
		formRow("Go", "", centreY(muted(runtime.Version()))),
		formRow("Platform", "", centreY(muted(fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)))),
	)
}

func (p *aboutPage) locations() fyne.CanvasObject {
	return formGrid(
		formRow("Data directory",
			"Override it with TASK_TIMER_DATA_DIR.",
			pathValue(task.DataDir())),
		formRow("Database", "", pathValue(task.DBPath())),
		formRow("Config file", "", pathValue(reconcile.ConfigPath())),
	)
}

// credits carries the attribution the vendored font's licence requires. The SIL
// Open Font License asks that the copyright notice and the licence travel with
// the font, and a font compiled into a binary travels wherever the binary does —
// so the notice belongs in the app, not only in the repository.
func (p *aboutPage) credits() fyne.CanvasObject {
	return formGrid(
		formRow("Typeface",
			"Inter 3.19 © 2016-2020 The Inter Project Authors, used under the SIL Open Font License 1.1.",
			centreY(muted("rsms/inter"))),
		formRow("Toolkit", "", centreY(muted("Fyne v2"))),
	)
}

// refresh satisfies the page contract. Nothing here changes at runtime.
func (p *aboutPage) refresh() {}
