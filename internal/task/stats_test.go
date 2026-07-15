package task

import (
	"testing"
	"time"
)

// at builds a session of the given length starting on the day daysAgo back.
func at(name string, daysAgo int, d time.Duration, now time.Time) Task {
	start := now.AddDate(0, 0, -daysAgo)
	return Task{Name: name, Start: start, End: start.Add(d), Duration: d}
}

func TestSummarize(t *testing.T) {
	now := time.Now()
	sessions := []Task{
		at("alpha", 0, time.Hour, now),
		at("alpha", 0, 30*time.Minute, now),
		at("beta", 1, 2*time.Hour, now),
		// An empty name is a row an older version wrote; it is not a task and
		// must not inflate the distinct-task count.
		at("", 1, 15*time.Minute, now),
	}

	got := Summarize(sessions)

	if want := 3*time.Hour + 45*time.Minute; got.Total != want {
		t.Errorf("Total = %s, want %s", got.Total, want)
	}
	if got.Sessions != 4 {
		t.Errorf("Sessions = %d, want 4", got.Sessions)
	}
	if got.Tasks != 2 {
		t.Errorf("Tasks = %d, want 2 (the unnamed row is not a task)", got.Tasks)
	}
	if want := 2 * time.Hour; got.Longest != want {
		t.Errorf("Longest = %s, want %s", got.Longest, want)
	}
	if want := (3*time.Hour + 45*time.Minute) / 4; got.Average != want {
		t.Errorf("Average = %s, want %s", got.Average, want)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	got := Summarize(nil)

	// Average divides by the session count; an empty slice must not panic.
	if got != (Summary{}) {
		t.Errorf("Summarize(nil) = %+v, want the zero Summary", got)
	}
}

func TestTotalsByNameOrdersByDurationThenName(t *testing.T) {
	now := time.Now()
	totals := TotalsByName([]Task{
		at("beta", 0, time.Hour, now),
		at("alpha", 0, time.Hour, now),
		at("gamma", 0, 3*time.Hour, now),
		at("alpha", 0, 30*time.Minute, now),
	})

	if len(totals) != 3 {
		t.Fatalf("got %d totals, want 3: %+v", len(totals), totals)
	}

	// gamma (3h), alpha (1h30m), beta (1h).
	want := []string{"gamma", "alpha", "beta"}
	for i, label := range want {
		if totals[i].Label != label {
			t.Errorf("totals[%d].Label = %q, want %q", i, totals[i].Label, label)
		}
	}

	if totals[1].Sessions != 2 {
		t.Errorf("alpha rolled up %d sessions, want 2", totals[1].Sessions)
	}
}

func TestTotalsByNameTiesBreakOnNameForStableOrdering(t *testing.T) {
	now := time.Now()

	// Equal durations: without a tiebreak the order would follow Go's
	// randomised map iteration and the report would reshuffle on every refresh.
	first := TotalsByName([]Task{
		at("zulu", 0, time.Hour, now),
		at("alpha", 0, time.Hour, now),
	})
	for i := 0; i < 20; i++ {
		again := TotalsByName([]Task{
			at("zulu", 0, time.Hour, now),
			at("alpha", 0, time.Hour, now),
		})
		if again[0].Label != first[0].Label || again[1].Label != first[1].Label {
			t.Fatalf("ordering is not stable: %q,%q then %q,%q",
				first[0].Label, first[1].Label, again[0].Label, again[1].Label)
		}
	}
	if first[0].Label != "alpha" {
		t.Errorf("tie broke to %q, want the lexicographically smaller %q", first[0].Label, "alpha")
	}
}

func TestTotalsBySourceLabelsUnsourcedRows(t *testing.T) {
	now := time.Now()

	local := at("alpha", 0, time.Hour, now) // Source left empty, as old rows have
	remote := at("ENG-1", 0, time.Hour, now)
	remote.Source = "gateway"

	totals := TotalsBySource([]Task{local, remote})

	labels := map[string]bool{}
	for _, total := range totals {
		labels[total.Label] = true
	}
	if !labels[SourceUserAdded] {
		t.Errorf("a session with no source should be reported as %q; got %+v", SourceUserAdded, totals)
	}
	if !labels["gateway"] {
		t.Errorf("missing the gateway source; got %+v", totals)
	}
}

func TestDailyTotalsKeepsIdleDays(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.Local)

	days := DailyTotals([]Task{
		at("alpha", 0, time.Hour, now),
		at("beta", 2, 2*time.Hour, now),
	}, 5, now)

	if len(days) != 5 {
		t.Fatalf("got %d days, want 5", len(days))
	}

	// Oldest first, so the window is Jul 10..Jul 14 and today is last.
	if !days[0].Date.Equal(dayStart(now.AddDate(0, 0, -4))) {
		t.Errorf("window starts at %s, want %s", days[0].Date, dayStart(now.AddDate(0, 0, -4)))
	}
	if days[4].Duration != time.Hour {
		t.Errorf("today = %s, want 1h", days[4].Duration)
	}
	if days[2].Duration != 2*time.Hour {
		t.Errorf("two days ago = %s, want 2h", days[2].Duration)
	}

	// The idle days must still be present — a chart that silently drops them
	// misrepresents the axis.
	for _, i := range []int{1, 3} {
		if days[i].Duration != 0 {
			t.Errorf("days[%d] = %s, want an idle day", i, days[i].Duration)
		}
	}
}

func TestDailyTotalsIgnoresSessionsOutsideTheWindow(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.Local)

	days := DailyTotals([]Task{
		at("old", 30, time.Hour, now),
		at("recent", 1, time.Hour, now),
	}, 7, now)

	var total time.Duration
	for _, d := range days {
		total += d.Duration
	}
	if total != time.Hour {
		t.Errorf("windowed total = %s, want 1h — the 30-day-old session leaked in", total)
	}
}

func TestPeak(t *testing.T) {
	if got := Peak(nil); got != 0 {
		t.Errorf("Peak(nil) = %s, want 0", got)
	}

	got := Peak([]Day{{Duration: time.Hour}, {Duration: 3 * time.Hour}, {}})
	if want := 3 * time.Hour; got != want {
		t.Errorf("Peak = %s, want %s", got, want)
	}
}
