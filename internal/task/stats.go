package task

import (
	"sort"
	"time"
)

// The aggregations here are pure functions over a slice of sessions rather than
// SQL. A personal time tracker holds thousands of rows, not millions, so the
// store fetches a date range once and the reports are derived in memory. That
// keeps the aggregation testable without a database and stops the Reports page
// from turning into a pile of near-duplicate queries.

// Summary is the headline set of figures for a range of sessions.
type Summary struct {
	Total    time.Duration
	Sessions int
	// Tasks counts distinct task names.
	Tasks   int
	Longest time.Duration
	Average time.Duration
}

// Summarize reduces sessions to their headline figures.
func Summarize(ts []Task) Summary {
	s := Summary{Sessions: len(ts)}

	names := make(map[string]bool, len(ts))
	for _, t := range ts {
		s.Total += t.Duration
		if t.Duration > s.Longest {
			s.Longest = t.Duration
		}
		names[t.Name] = true
	}
	// An empty name is not a task; it is a row written by an older version.
	delete(names, "")
	s.Tasks = len(names)

	if s.Sessions > 0 {
		s.Average = s.Total / time.Duration(s.Sessions)
	}
	return s
}

// Total is a labelled duration: time spent per task, per source, per day.
type Total struct {
	Label    string
	Duration time.Duration
	// Sessions is how many work sessions rolled up into Duration.
	Sessions int
}

// TotalsByName sums time per task name, longest first. Ties break on name so
// the ordering is stable across reloads rather than jittering with map order.
func TotalsByName(ts []Task) []Total {
	return totalsBy(ts, func(t Task) string { return t.Name })
}

// TotalsBySource sums time per task source: SourceUserAdded, or a provider name.
func TotalsBySource(ts []Task) []Total {
	return totalsBy(ts, func(t Task) string {
		if t.Source == "" {
			return SourceUserAdded
		}
		return t.Source
	})
}

func totalsBy(ts []Task, key func(Task) string) []Total {
	index := map[string]*Total{}
	for _, t := range ts {
		k := key(t)
		if k == "" {
			continue
		}
		if index[k] == nil {
			index[k] = &Total{Label: k}
		}
		index[k].Duration += t.Duration
		index[k].Sessions++
	}

	out := make([]Total, 0, len(index))
	for _, total := range index {
		out = append(out, *total)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Duration != out[j].Duration {
			return out[i].Duration > out[j].Duration
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// Day is one bar of the daily chart.
type Day struct {
	Date     time.Time
	Duration time.Duration
	Sessions int
}

// DailyTotals buckets sessions into the `days` calendar days ending on the day
// containing `end`, oldest first. Days with no work are present with a zero
// duration: the chart needs the gaps to keep its x-axis honest.
func DailyTotals(ts []Task, days int, end time.Time) []Day {
	if days < 1 {
		return nil
	}

	last := dayStart(end)
	first := last.AddDate(0, 0, -(days - 1))

	out := make([]Day, days)
	index := make(map[string]int, days)
	for i := range out {
		date := first.AddDate(0, 0, i)
		out[i] = Day{Date: date}
		index[date.Format(time.DateOnly)] = i
	}

	for _, t := range ts {
		if t.Start.IsZero() {
			continue
		}
		i, ok := index[t.Start.Local().Format(time.DateOnly)]
		if !ok {
			continue // outside the window
		}
		out[i].Duration += t.Duration
		out[i].Sessions++
	}
	return out
}

// Peak returns the largest duration in the series, used to scale a chart's
// bars. It is zero when the series is empty or entirely idle, and callers must
// treat that as "draw nothing" rather than dividing by it.
func Peak(days []Day) time.Duration {
	var peak time.Duration
	for _, d := range days {
		if d.Duration > peak {
			peak = d.Duration
		}
	}
	return peak
}

func dayStart(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
