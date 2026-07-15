package ui

import (
	"errors"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// tasksPage is the full session history. The Dashboard's table is the same
// component, but this page carries its own filter and search so switching to it
// does not disturb whatever the Dashboard was showing, and it adds a "start
// timing this" action the Dashboard has no room for.
type tasksPage struct {
	app     *App
	content fyne.CanvasObject

	filter *widget.Select
	search *widget.Entry
	count  *fyne.Container

	table *taskTable
}

func newTasksPage(a *App) *tasksPage {
	p := &tasksPage{app: a}

	p.count = container.NewHBox()
	p.table = newTaskTable(a, "Nothing recorded yet.")

	// Selection first, callback second — SetSelected fires OnChanged, and a
	// refresh before the table exists is a nil dereference.
	p.filter = widget.NewSelect(filterOptions, nil)
	p.filter.SetSelected(filterAll)
	p.filter.OnChanged = func(string) { p.refresh() }

	p.search = widget.NewEntry()
	p.search.SetPlaceHolder("Search by task, assignee, or comment…")
	p.search.OnChanged = func(string) { p.refresh() }

	newBtn := iconButton("New Task", iconPlus, true, p.newTask)

	toolbar := container.NewBorder(nil, nil,
		container.NewHBox(
			sized(p.filter, 150, 36),
			sized(searchField(p.search), 340, 36),
		),
		container.NewHBox(centreY(p.count), insetXY(centreY(newBtn), 12, 0)),
	)

	p.content = container.NewBorder(
		insetXY(toolbar, 0, 0), nil, nil, nil,
		container.New(insetLayout{top: 14}, p.table.content),
	)
	return p
}

func (p *tasksPage) refresh() {
	shown := filterTasks(p.app.tasks, p.filter.Selected, p.search.Text)
	p.table.set(shown, nil)

	label := "sessions"
	if len(shown) == 1 {
		label = "session"
	}

	// The count doubles as the honest answer to "is the filter hiding things?".
	summary := muted(fmt.Sprintf("%d %s of %d", len(shown), label, len(p.app.tasks)))

	p.count.Objects = []fyne.CanvasObject{summary}
	p.count.Refresh()
}

// newTask prompts for a name and starts timing it. A task only becomes real once
// it has a session, so "create" here means "start the clock on it": the name is
// handed to the timer and the user is taken to the Overview to see it running.
func (p *tasksPage) newTask() {
	entry := widget.NewEntry()
	entry.SetPlaceHolder("Task name")
	entry.Validator = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("a task name is required")
		}
		return nil
	}

	items := []*widget.FormItem{widget.NewFormItem("Name", entry)}
	d := dialog.NewForm("New Task", "Start timing", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		name := strings.TrimSpace(entry.Text)
		if name == "" {
			return
		}
		// SwitchTask stops anything already running, then starts this one.
		p.app.SwitchTask(name)
		p.app.selectPage(0)
	}, p.app.window)

	d.Resize(fyne.NewSize(420, d.MinSize().Height))
	d.Show()
}
