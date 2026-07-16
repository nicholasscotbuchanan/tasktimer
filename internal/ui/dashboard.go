package ui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"task-timer-app/internal/task"
)

// Filter names. "All Tasks" is first so it is the Select's default.
const (
	filterAll       = "All Tasks"
	filterOpen      = "Open"
	filterCompleted = "Completed"
	filterPushed    = "Pushed"
	filterToday     = "Today"
)

var filterOptions = []string{filterAll, filterOpen, filterCompleted, filterPushed, filterToday}

// dashboardPage is the Overview: the running timer, today's totals, and the
// session table.
type dashboardPage struct {
	app     *App
	content fyne.CanvasObject

	timerText *canvas.Text
	taskEntry *widget.SelectEntry
	startBtn  *widget.Button
	lapBtn    *widget.Button
	resetBtn  *widget.Button
	openBtn   *widget.Button

	sumTotal     *canvas.Text
	sumRemaining *canvas.Text
	sumTasks     *canvas.Text

	filter *widget.Select
	search *widget.Entry

	table *taskTable
}

func newDashboardPage(a *App) *dashboardPage {
	p := &dashboardPage{app: a}

	p.buildTimer()
	p.buildSummary()
	p.buildToolbar()
	p.table = newTaskTable(a, "No sessions yet. Name a task above and press Start.")

	// The two cards share a row: the timer takes the space, the summary keeps a
	// fixed width so its figures stay column-aligned as the window grows.
	summary := sized(card("Today's Summary", p.summaryContent()), 340, 0)
	top := container.NewBorder(nil, nil, nil, summary,
		card("Current Timer", p.timerContent()))

	head := container.NewVBox(
		top,
		insetXY(p.toolbar(), 0, 14),
	)

	p.content = container.NewBorder(head, nil, nil, nil, p.table.content)
	return p
}

// ---------------------------------------------------------------------------
// Timer card
// ---------------------------------------------------------------------------

// timerTextSize is chosen so that "00:00:00.000" and the three controls beside
// it still fit inside the timer card when the window is at its narrowest — which
// the table's column minimums peg at a little over 1240px. Inter sets wider
// digits than Fyne's stock face, so a size that fitted before does not now.
const timerTextSize float32 = 44

func (p *dashboardPage) buildTimer() {
	p.timerText = canvas.NewText(stopwatch(0), colText)
	p.timerText.TextSize = timerTextSize
	p.timerText.TextStyle.Bold = true

	p.taskEntry = widget.NewSelectEntry(nil)
	p.taskEntry.SetPlaceHolder("Enter a task name, or pick one from your tracker")
	p.taskEntry.OnChanged = func(text string) {
		p.refreshButtons()
	}

	p.startBtn = iconButton("Start", iconPlay, true, p.toggle)
	p.lapBtn = iconButton("Lap", iconFlag, false, p.app.Lap)
	p.resetBtn = iconButton("Reset", iconReset, false, p.app.Reset)

	// Only meaningful for a task that came from a provider, so it stays disabled
	// until the entry names one.
	p.openBtn = iconButton("Open in tracker", iconExternal, false, func() {
		p.app.OpenRemote(p.taskName())
	})

	p.refreshButtons()
}

func (p *dashboardPage) timerContent() fyne.CanvasObject {
	controls := container.NewHBox(p.startBtn, p.lapBtn, p.resetBtn)

	clockRow := container.NewBorder(nil, nil, nil, centreY(controls),
		centreY(p.timerText))

	entryRow := container.NewBorder(nil, nil, nil, p.openBtn, p.taskEntry)

	return container.NewVBox(
		clockRow,
		insetXY(entryRow, 0, 10),
	)
}

// refreshButtons puts the controls into the state the timer is actually in.
func (p *dashboardPage) refreshButtons() {
	running := p.app.running
	named := strings.TrimSpace(p.taskName()) != ""

	if running {
		p.startBtn.SetText("Stop")
		p.startBtn.SetIcon(iconStop(colText))
		p.startBtn.Importance = widget.DangerImportance
		p.startBtn.Enable()
	} else {
		p.startBtn.SetText("Start")
		p.startBtn.SetIcon(iconPlay(colText))
		p.startBtn.Importance = widget.HighImportance
		if named {
			p.startBtn.Enable()
		} else {
			p.startBtn.Disable()
		}
	}
	p.startBtn.Refresh()

	// A lap only means something mid-session; a reset is only ever needed then
	// too, since a stopped clock already reads zero.
	if running {
		p.lapBtn.Enable()
		p.resetBtn.Enable()
		p.taskEntry.Disable()
	} else {
		p.lapBtn.Disable()
		p.resetBtn.Disable()
		p.taskEntry.Enable()
	}

	if r, ok := p.app.remotes[p.taskName()]; ok && r.URL != "" {
		p.openBtn.Enable()
	} else {
		p.openBtn.Disable()
	}
}

func (p *dashboardPage) toggle() {
	if p.app.running {
		p.app.Stop()
		return
	}
	p.app.Start(p.taskName())
}

func (p *dashboardPage) taskName() string {
	return strings.TrimSpace(p.taskEntry.Text)
}

func (p *dashboardPage) setTaskName(name string) {
	p.taskEntry.SetText(name)
}

// timerStarted, timerStopped and timerReset are the App's hooks into the card.
func (p *dashboardPage) timerStarted() {
	p.refreshButtons()
	p.refresh()
}

func (p *dashboardPage) timerStopped() {
	p.refreshButtons()
}

func (p *dashboardPage) timerReset() {
	p.showElapsed(0)
	p.refreshButtons()
	p.refresh()
}

// showElapsed is called from the ticker goroutine.
func (p *dashboardPage) showElapsed(d time.Duration) {
	setText(p.timerText, stopwatch(d))
}

// ---------------------------------------------------------------------------
// Summary card
// ---------------------------------------------------------------------------

func (p *dashboardPage) buildSummary() {
	p.sumTotal = summaryValue(clock(0), colText)
	p.sumRemaining = summaryValue(clock(0), colAccent)
	p.sumTasks = summaryValue("0", colText)
}

func (p *dashboardPage) summaryContent() fyne.CanvasObject {
	return container.NewVBox(
		summaryRow("Total Time", p.sumTotal),
		insetXY(hairline(), 0, 8),
		summaryRow("Time Remaining", p.sumRemaining),
		insetXY(hairline(), 0, 8),
		summaryRow("Tasks Logged", p.sumTasks),
	)
}

// summaryValue is the bold figure on the right of a summary row.
func summaryValue(text string, c color.Color) *canvas.Text {
	t := canvas.NewText(text, c)
	t.TextSize = 14
	t.TextStyle.Bold = true
	t.Alignment = fyne.TextAlignTrailing
	return t
}

// summaryRow is a muted label on the left, a figure on the right.
func summaryRow(label string, value *canvas.Text) fyne.CanvasObject {
	return container.NewBorder(nil, nil, centreY(muted(label)), centreY(value))
}

// showSummary refreshes the three figures. The remaining-time row flips to an
// overtime reading once the working day is used up, rather than counting into
// negative numbers.
func (p *dashboardPage) showSummary(total, target time.Duration, count int) {
	setText(p.sumTotal, clock(total))
	setText(p.sumTasks, fmt.Sprintf("%d", count))

	if total >= target {
		setText(p.sumRemaining, "+"+clock(total-target))
		p.sumRemaining.Color = colDanger
	} else {
		setText(p.sumRemaining, clock(target-total))
		p.sumRemaining.Color = colAccent
	}
	p.sumRemaining.Refresh()
}

// ---------------------------------------------------------------------------
// Toolbar and table
// ---------------------------------------------------------------------------

func (p *dashboardPage) buildToolbar() {
	// The callback is attached after the initial selection, not passed to the
	// constructor: SetSelected fires OnChanged, and a refresh at construction
	// time would run against a page whose table does not exist yet.
	p.filter = widget.NewSelect(filterOptions, nil)
	p.filter.SetSelected(filterAll)
	p.filter.OnChanged = func(string) { p.refresh() }

	p.search = widget.NewEntry()
	p.search.SetPlaceHolder("Search tasks…")
	p.search.OnChanged = func(string) { p.refresh() }
}

func (p *dashboardPage) toolbar() fyne.CanvasObject {
	filterBox := sized(p.filter, 150, 36)
	searchBox := sized(searchField(p.search), 280, 36)

	refresh := iconButton("Refresh", iconRefresh, false, p.app.reload)
	sync := iconButton("Push", iconCheck, true, p.app.Push)

	return container.NewBorder(nil, nil,
		container.NewHBox(filterBox, searchBox),
		centreY(container.NewHBox(sync, refresh)),
	)
}

// refresh rebuilds the table from the App's cached sessions.
func (p *dashboardPage) refresh() {
	p.taskEntry.SetOptions(p.app.taskOptions())

	// Pre-fill with whatever was last worked on, so the common case — carrying
	// on with the same thing — is one click rather than a click and a retype.
	if p.taskName() == "" && !p.app.running {
		if recent, err := p.app.store.MostRecentName(); err == nil && recent != "" {
			p.taskEntry.SetText(recent)
		}
	}

	var pinned fyne.CanvasObject
	if p.app.running {
		source := task.SourceUserAdded
		if r, ok := p.app.remotes[p.taskName()]; ok {
			source = r.Provider
		}
		pinned = p.table.runningRow(p.taskName(), p.app.startTime, source)
	}

	p.table.set(filterTasks(p.app.tasks, p.filter.Selected, p.search.Text), pinned)
}

// filterTasks applies the toolbar's filter and search box. Search matches the
// task name, the person who assigned it, and the comment — the three fields a
// user would plausibly be hunting by.
func filterTasks(tasks []task.Task, filter, query string) []task.Task {
	query = strings.ToLower(strings.TrimSpace(query))

	out := make([]task.Task, 0, len(tasks))
	for _, t := range tasks {
		if !matchesFilter(t, filter) {
			continue
		}
		if query != "" && !matchesQuery(t, query) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func matchesFilter(t task.Task, filter string) bool {
	switch filter {
	case filterOpen:
		return !t.Completed()
	case filterCompleted:
		return t.Completed()
	case filterPushed:
		return t.PushSignature != ""
	case filterToday:
		return sameDay(t.Start, time.Now())
	default: // filterAll, and an empty selection at startup
		return true
	}
}

func matchesQuery(t task.Task, query string) bool {
	for _, field := range []string{t.Name, t.AssignedBy, t.Comment, t.Source} {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func sameDay(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	a, b = a.Local(), b.Local()
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
