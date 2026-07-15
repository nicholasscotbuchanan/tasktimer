package ui

import (
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"task-timer-app/internal/task"
)

// ranges are the report windows, in days back from today.
var ranges = []struct {
	label string
	days  int
}{
	{"Last 7 days", 7},
	{"Last 14 days", 14},
	{"Last 30 days", 30},
	{"Last 90 days", 90},
}

// reportsPage answers "where did the hours go": a daily column chart, headline
// figures, and breakdowns by task and by source.
//
// It queries the store directly for its window rather than reusing the App's
// cached recent sessions, which are capped and would silently truncate a
// 90-day report.
type reportsPage struct {
	app     *App
	content fyne.CanvasObject

	rangeSelect *widget.Select

	tiles     *fyne.Container
	chart     *fyne.Container
	chartNote *fyne.Container
	byTask    *fyne.Container
	bySource  *fyne.Container
}

func newReportsPage(a *App) *reportsPage {
	p := &reportsPage{
		app:       a,
		tiles:     container.NewGridWithColumns(4),
		chart:     container.NewStack(),
		chartNote: container.NewHBox(),
		byTask:    container.NewVBox(),
		bySource:  container.NewVBox(),
	}

	labels := make([]string, len(ranges))
	for i, r := range ranges {
		labels[i] = r.label
	}

	// Selection first, callback second: SetSelected fires OnChanged, and there
	// is no reason to run a store query while the page is still being built —
	// the router refreshes it on arrival.
	p.rangeSelect = widget.NewSelect(labels, nil)
	p.rangeSelect.SetSelected(ranges[0].label)
	p.rangeSelect.OnChanged = func(string) { p.refresh() }

	toolbar := container.NewBorder(nil, nil, nil,
		sized(p.rangeSelect, 170, 36),
		centreY(muted("Totals are computed from every session that started in the window.")),
	)

	chartCard := card("Time Per Day", container.NewBorder(
		nil, container.New(insetLayout{top: 10}, p.chartNote), nil, nil,
		p.chart,
	))

	breakdowns := container.NewGridWithColumns(2,
		card("Time Per Task", p.byTask),
		card("Time Per Source", p.bySource),
	)

	body := container.NewVBox(
		p.tiles,
		insetXY(chartCard, 0, 14),
		breakdowns,
	)

	// The right inset is the scrollbar's lane. Without it the bar is drawn over
	// the cards' right edge, clipping their border.
	p.content = container.NewBorder(
		container.New(insetLayout{bottom: 14}, toolbar), nil, nil, nil,
		container.NewVScroll(container.New(insetLayout{right: 10}, body)),
	)
	return p
}

// selectedRange is the window in days, defaulting to the first option.
func (p *reportsPage) selectedRange() int {
	for _, r := range ranges {
		if r.label == p.rangeSelect.Selected {
			return r.days
		}
	}
	return ranges[0].days
}

func (p *reportsPage) refresh() {
	days := p.selectedRange()

	now := time.Now()
	end := dayEnd(now)
	start := dayStart(now).AddDate(0, 0, -(days - 1))

	sessions, err := p.app.store.Between(start, end)
	if err != nil {
		p.app.reportError("Building report", err)
		return
	}

	p.showTiles(task.Summarize(sessions), days)
	p.showChart(task.DailyTotals(sessions, days, now))
	p.showBreakdown(p.byTask, task.TotalsByName(sessions), colPrimary, "No work recorded in this window.")
	p.showBreakdown(p.bySource, task.TotalsBySource(sessions), colAccent, "No sources to report.")
}

func (p *reportsPage) showTiles(s task.Summary, days int) {
	target := p.app.workingDay() * time.Duration(days)

	// A percentage against the working-day target only means something when a
	// target exists; guard the division rather than printing NaN%.
	utilisation := "—"
	if target > 0 {
		utilisation = fmt.Sprintf("%.0f%%", 100*float64(s.Total)/float64(target))
	}

	p.tiles.Objects = []fyne.CanvasObject{
		statTile("Total Tracked", clock(s.Total), colText),
		statTile("Sessions", fmt.Sprintf("%d", s.Sessions), colText),
		statTile("Distinct Tasks", fmt.Sprintf("%d", s.Tasks), colText),
		statTile("Against Target", utilisation, colAccent),
	}
	p.tiles.Refresh()
}

// maxLabelledDays is the point past which per-day captions stop being an axis
// and start being a smear.
const maxLabelledDays = 14

func (p *reportsPage) showChart(days []task.Day) {
	peak := task.Peak(days)

	fractions := make([]float64, len(days))
	for i, d := range days {
		if peak > 0 {
			fractions[i] = float64(d.Duration) / float64(peak)
		}
	}

	var labels []string
	if len(days) <= maxLabelledDays {
		labels = make([]string, len(days))
		for i, d := range days {
			labels[i] = d.Date.Format("Mon")
		}
	}

	p.chart.Objects = []fyne.CanvasObject{newColumnChart(fractions, labels, colPrimary)}
	p.chart.Refresh()

	note := "Nothing tracked in this window."
	if peak > 0 {
		note = fmt.Sprintf("%s – %s   ·   busiest day %s",
			days[0].Date.Format("Jan 2"),
			days[len(days)-1].Date.Format("Jan 2"),
			clock(peak))
	}

	p.chartNote.Objects = []fyne.CanvasObject{muted(note)}
	p.chartNote.Refresh()
}

// showBreakdown fills a card with labelled proportion bars, longest first. The
// list is capped: a breakdown is meant to show where the time concentrates, and
// forty one-minute rows do not.
func (p *reportsPage) showBreakdown(into *fyne.Container, totals []task.Total, fill color.Color, empty string) {
	const maxRows = 8

	if len(totals) == 0 {
		into.Objects = []fyne.CanvasObject{muted(empty)}
		into.Refresh()
		return
	}

	peak := totals[0].Duration // sorted descending, so the first is the largest

	rows := make([]fyne.CanvasObject, 0, maxRows+1)
	for i, t := range totals {
		if i == maxRows {
			rows = append(rows, insetXY(muted(fmt.Sprintf("+ %d more", len(totals)-maxRows)), 0, 4))
			break
		}

		var fraction float64
		if peak > 0 {
			fraction = float64(t.Duration) / float64(peak)
		}

		name := widget.NewLabel(t.Label)
		name.Truncation = fyne.TextTruncateEllipsis

		value := canvas.NewText(clock(t.Duration), colTextMuted)
		value.TextSize = 12
		value.Alignment = fyne.TextAlignTrailing

		header := container.NewBorder(nil, nil, nil, centreY(value), name)

		rows = append(rows, container.NewVBox(
			header,
			container.New(insetLayout{bottom: 12}, meter(fraction, fill)),
		))
	}

	into.Objects = rows
	into.Refresh()
}

// statTile is one of the headline figures across the top of the report.
func statTile(label, value string, c color.Color) fyne.CanvasObject {
	figure := canvas.NewText(value, c)
	figure.TextSize = 24
	figure.TextStyle.Bold = true

	body := container.NewVBox(
		sectionLabel(label),
		insetXY(figure, 0, 6),
	)
	return container.NewStack(surface(colCard, radiusCard), inset(body, 16))
}

// dayEnd is the exclusive upper bound of the day containing t.
func dayEnd(t time.Time) time.Time {
	return dayStart(t).AddDate(0, 0, 1)
}

// dayStart mirrors the store's notion of a calendar day.
func dayStart(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
