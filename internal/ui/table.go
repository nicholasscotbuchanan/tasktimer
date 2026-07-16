package ui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"task-timer-app/internal/task"
)

// The table is shared: the Dashboard shows today's work in it and the Tasks page
// shows everything, but a session renders identically in both. The columns are
// declared once, and the header and every row lay out against the same grid —
// which is what keeps them aligned.
//
// Each column carries a floor as well as a weight. The floors are sized to the
// widest thing the column can actually hold — the longest status is
// "Pushed — Complete", the widest action is the "Complete" button — because
// neither a pill nor a button can ellipsise its way out of a column that is too
// narrow. Only Task, Assigned By and Comment truncate, so only they are allowed
// to be squeezed.
var tableColumns = []struct {
	title string
	column
}{
	{"#", column{min: 28, weight: 0.2}},
	{"Task", column{min: 140, weight: 2.6}},
	{"Start", column{min: 80, weight: 0.7}},
	{"End", column{min: 80, weight: 0.7}},
	{"Duration", column{min: 58, weight: 0.5}},
	{"Assigned By", column{min: 80, weight: 0.9}},
	{"Source", column{min: 80, weight: 0.6}},
	{"Status", column{min: 150, weight: 0.6}},
	{"Comment", column{min: 64, weight: 0.9}},
	{"Action", column{min: 108, weight: 0.3}},
}

const (
	cellPadX float32 = 14
	cellPadY float32 = 11
	colGap   float32 = 8
)

func tableGrid() columnsLayout {
	cols := make([]column, len(tableColumns))
	for i, c := range tableColumns {
		cols[i] = c.column
	}
	return columnsLayout{columns: cols, spacing: colGap}
}

// taskTable is the scrolling list of work sessions inside a card.
type taskTable struct {
	app  *App
	rows *fyne.Container

	// content is the whole card: header strip above, scrolling body below.
	content fyne.CanvasObject

	// empty is shown in place of the rows when there is nothing to list.
	empty *fyne.Container

	// headers is one clickable header per column, so a tap can re-sort the rows
	// and the arrow indicator can be moved to the active column.
	headers []*sortHeader

	// sortCol is the column index the rows are sorted by, or -1 for the store's
	// own order (newest first). sortAsc flips on a second tap of the same header.
	sortCol int
	sortAsc bool

	// last is what set() was called with, kept so a header tap can re-sort the
	// same data without the page having to hand it over again.
	last   []task.Task
	pinned fyne.CanvasObject
}

func newTaskTable(a *App, emptyMessage string) *taskTable {
	t := &taskTable{app: a, rows: container.NewVBox(), sortCol: -1}

	t.empty = container.NewCenter(inset(muted(emptyMessage), 28))
	t.empty.Hide()

	body := container.NewVScroll(container.NewVBox(t.rows, t.empty))

	// The header sits on its own strip, so the rows scroll underneath it rather
	// than taking it with them.
	stack := container.NewBorder(t.header(), nil, nil, nil, body)
	t.content = container.NewStack(surface(colCard, radiusCard), inset(stack, 1))

	return t
}

func (t *taskTable) header() fyne.CanvasObject {
	t.headers = make([]*sortHeader, len(tableColumns))

	cells := make([]fyne.CanvasObject, 0, len(tableColumns))
	for i, c := range tableColumns {
		col := i
		h := newSortHeader(c.title, sortableColumn(col), func() { t.sortBy(col) })
		t.headers[i] = h
		cells = append(cells, h)
	}

	row := container.New(tableGrid(), cells...)

	strip := container.NewStack(
		canvas.NewRectangle(colCardAlt),
		container.New(insetLayout{top: 12, right: cellPadX, bottom: 12, left: cellPadX}, row),
	)
	return container.NewBorder(nil, hairline(), nil, nil, strip)
}

// sortBy re-sorts the table by a column. Tapping the active column again flips
// the direction; tapping a new one starts ascending.
func (t *taskTable) sortBy(col int) {
	if !sortableColumn(col) {
		return
	}
	if t.sortCol == col {
		t.sortAsc = !t.sortAsc
	} else {
		t.sortCol = col
		t.sortAsc = true
	}
	t.updateIndicators()
	t.set(t.last, t.pinned)
}

// updateIndicators moves the arrow to the sorted column and clears the rest.
func (t *taskTable) updateIndicators() {
	for i, h := range t.headers {
		switch {
		case i != t.sortCol:
			h.setIndicator(indNone)
		case t.sortAsc:
			h.setIndicator(indAsc)
		default:
			h.setIndicator(indDesc)
		}
	}
}

// set replaces the rows. A non-nil pinned object — the session currently being
// timed — is held at the top of the list, above the sorted sessions.
func (t *taskTable) set(tasks []task.Task, pinned fyne.CanvasObject) {
	t.last = tasks
	t.pinned = pinned

	t.rows.Objects = nil

	if pinned != nil {
		t.rows.Add(pinned)
	}
	for _, session := range t.sorted(tasks) {
		t.rows.Add(t.row(session))
	}

	if len(tasks) == 0 && pinned == nil {
		t.empty.Show()
	} else {
		t.empty.Hide()
	}

	t.rows.Refresh()
	t.empty.Refresh()
}

// sorted returns tasks in the table's current sort order. With no column chosen
// it returns them untouched — the store already hands them over newest first.
func (t *taskTable) sorted(tasks []task.Task) []task.Task {
	if t.sortCol < 0 {
		return tasks
	}
	out := make([]task.Task, len(tasks))
	copy(out, tasks)
	sort.SliceStable(out, func(i, j int) bool {
		if t.sortAsc {
			return taskLess(out[i], out[j], t.sortCol)
		}
		return taskLess(out[j], out[i], t.sortCol)
	})
	return out
}

// sortableColumn reports whether a column can be sorted by. Everything except
// the trailing Action column, which holds buttons rather than data, can.
func sortableColumn(col int) bool {
	return col >= 0 && col < len(tableColumns)-1
}

// taskLess orders two sessions by a column, matching the order the cells render
// in. Text comparisons are case-insensitive so "apex" and "Apex" sort together.
func taskLess(a, b task.Task, col int) bool {
	switch col {
	case 0:
		return a.Instance < b.Instance
	case 1:
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	case 2:
		return a.Start.Before(b.Start)
	case 3:
		return a.End.Before(b.End)
	case 4:
		return a.Duration < b.Duration
	case 5:
		return strings.ToLower(a.AssignedBy) < strings.ToLower(b.AssignedBy)
	case 6:
		return strings.ToLower(a.Source) < strings.ToLower(b.Source)
	case 7:
		return strings.ToLower(string(a.Status)) < strings.ToLower(string(b.Status))
	case 8:
		return strings.ToLower(a.Comment) < strings.ToLower(b.Comment)
	}
	return false
}

// row renders one work session. Completed sessions are dimmed throughout.
func (t *taskTable) row(session task.Task) fyne.CanvasObject {
	done := session.Completed()

	cells := []fyne.CanvasObject{
		centreY(textCell(strconv.Itoa(session.Instance), done)),
		centreY(truncatingCell(session.Name, done)),
		centreY(stampCell(session.Start, done)),
		centreY(stampCell(session.End, done)),
		centreY(textCell(humanDuration(session.Duration), done)),
		centreY(truncatingCell(session.AssignedBy, done)),
		centreY(textCell(session.Source, done)),
		hug(statusPill(string(session.Status), done)),
		centreY(truncatingCell(session.Comment, done)),
		hug(t.action(session)),
	}

	row := container.New(tableGrid(), cells...)
	padded := container.New(insetLayout{top: cellPadY, right: cellPadX, bottom: cellPadY, left: cellPadX}, row)

	return container.NewBorder(nil, hairline(), nil, nil, padded)
}

// action is the row's button: complete open work, reopen finished work.
func (t *taskTable) action(session task.Task) fyne.CanvasObject {
	id, name := session.ID, session.Name

	if session.Completed() {
		return iconButton("Reopen", iconReset, false, func() {
			t.app.Reopen(id, name)
		})
	}
	return iconButton("Complete", iconCheck, true, func() {
		t.app.Complete(id, name)
	})
}

// runningRow is the placeholder pinned to the top while a timer is going. It is
// deliberately not a real row: nothing has been written to the store yet.
func (t *taskTable) runningRow(name string, start time.Time, source string) fyne.CanvasObject {
	cells := []fyne.CanvasObject{
		centreY(textCell("•", false)),
		centreY(truncatingCell(name, false)),
		centreY(stampCell(start, false)),
		centreY(textCell("running…", false)),
		centreY(textCell("running…", false)),
		centreY(textCell(t.app.user, false)),
		centreY(textCell(source, false)),
		hug(statusPill(string(task.StatusInProgress), false)),
		centreY(textCell("", false)),
		centreY(textCell("", false)),
	}

	row := container.New(tableGrid(), cells...)
	padded := container.New(insetLayout{top: cellPadY, right: cellPadX, bottom: cellPadY, left: cellPadX}, row)

	// Tinted, so the row that is still accruing time is distinguishable from the
	// ones that are finished.
	tint := canvas.NewRectangle(rgba(0x2F6BEB, 0x1A))

	return container.NewBorder(nil, hairline(), nil, nil,
		container.NewStack(tint, padded))
}

// ---------------------------------------------------------------------------
// Cells
// ---------------------------------------------------------------------------

// textCell is a plain, non-truncating cell.
func textCell(text string, done bool) fyne.CanvasObject {
	t := canvas.NewText(text, colText)
	t.TextSize = 13
	if done {
		t.Color = colTextDim
	}
	return t
}

// truncatingCell ellipsises rather than overflowing into the next column, which
// is what a 90-character issue summary would otherwise do.
func truncatingCell(text string, done bool) fyne.CanvasObject {
	l := widget.NewLabel(text)
	l.Truncation = fyne.TextTruncateEllipsis
	if done {
		l.Importance = widget.LowImportance
	}
	return l
}

// ---------------------------------------------------------------------------
// Sortable header
// ---------------------------------------------------------------------------

// Sort indicator states for a column header.
const (
	indNone = iota // not the sorted column
	indAsc         // sorted ascending
	indDesc        // sorted descending
)

// sortHeader is a clickable column header. Tapping it asks the table to sort by
// that column; a second tap flips the direction. It carries the same styling as
// the static labels it replaces, plus an arrow marking the active sort.
//
// It is a widget rather than a bare canvas.Text because canvas objects are not
// tappable, and the whole column heading — not just the glyphs — needs to be a
// hit target.
type sortHeader struct {
	widget.BaseWidget

	title    string
	sortable bool
	onTap    func()
	text     *canvas.Text
}

func newSortHeader(title string, sortable bool, onTap func()) *sortHeader {
	h := &sortHeader{title: title, sortable: sortable, onTap: onTap}

	h.text = canvas.NewText(title, colTextMuted)
	h.text.TextSize = 12
	h.text.TextStyle.Bold = true

	h.ExtendBaseWidget(h)
	return h
}

// setIndicator appends an arrow to the label and brightens it when this column
// is the one being sorted by.
func (h *sortHeader) setIndicator(state int) {
	switch state {
	case indAsc:
		h.text.Text = h.title + "  ↑"
		h.text.Color = colText
	case indDesc:
		h.text.Text = h.title + "  ↓"
		h.text.Color = colText
	default:
		h.text.Text = h.title
		h.text.Color = colTextMuted
	}
	h.text.Refresh()
}

func (h *sortHeader) Tapped(*fyne.PointEvent) {
	if h.sortable && h.onTap != nil {
		h.onTap()
	}
}

// Cursor turns the pointer into a hand over sortable columns, so it reads as
// something you can click.
func (h *sortHeader) Cursor() desktop.Cursor {
	if h.sortable {
		return desktop.PointerCursor
	}
	return desktop.DefaultCursor
}

func (h *sortHeader) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(centreY(h.text))
}

var (
	_ fyne.Tappable      = (*sortHeader)(nil)
	_ desktop.Cursorable = (*sortHeader)(nil)
)

// stampCell stacks the date over the time, keeping the timestamp columns narrow.
func stampCell(at time.Time, done bool) fyne.CanvasObject {
	date := canvas.NewText(formatDate(at), colText)
	date.TextSize = 13

	clockText := canvas.NewText(formatClock(at), colTextMuted)
	clockText.TextSize = 12

	if done {
		date.Color = colTextDim
		clockText.Color = colTextDim
	}

	return container.NewVBox(date, clockText)
}
