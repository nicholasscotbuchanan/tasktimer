package ui

import (
	"fmt"
	"time"
)

// dateLayout and clockLayout split what the old single timestamp format did in
// one line. The table renders a session's start and end as two stacked lines —
// date above, time below — which is what keeps those columns narrow.
const (
	dateLayout  = "01/02/2006"
	clockLayout = "03:04:05 PM"
)

// clock renders a duration as HH:MM:SS. Hours are not wrapped at 24: a total of
// "26:10:00" is a meaningful thing for a timesheet to say.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d",
		int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

// stopwatch renders the running timer, down to milliseconds.
func stopwatch(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d.%03d",
		int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, d.Milliseconds()%1000)
}

// humanDuration renders a session length for the table.
//
// The column used to show time.Duration.String(), which prints a five-second
// session as "4.764048375s". Nanosecond precision is noise in a timesheet, so
// this rounds to something a person can read at a glance.
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// formatDate and formatClock render the two halves of a timestamp cell. A zero
// time — an unfinished session, or a row an older version wrote without one —
// renders as an em dash rather than "01/01/0001".
func formatDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format(dateLayout)
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(clockLayout)
}
