package ui

import (
	"testing"
	"time"

	"task-timer-app/internal/task"
)

// names pulls the task names out in order, for terse assertions.
func names(tasks []task.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.Name
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sample is three sessions whose columns disagree on order, so a sort by one
// column is visibly different from a sort by another.
func sample() []task.Task {
	base := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	return []task.Task{
		{Name: "Banana", Start: base.Add(2 * time.Hour), Duration: 30 * time.Minute},
		{Name: "apple", Start: base.Add(1 * time.Hour), Duration: 90 * time.Minute},
		{Name: "Cherry", Start: base, Duration: 5 * time.Minute},
	}
}

// TestSortedIsUntouchedByDefault: with no column chosen the table preserves the
// store's own order, which is how the pages looked before sorting existed.
func TestSortedIsUntouchedByDefault(t *testing.T) {
	tbl := &taskTable{sortCol: -1}
	got := names(tbl.sorted(sample()))
	want := []string{"Banana", "apple", "Cherry"}
	if !equal(got, want) {
		t.Errorf("default order changed: got %v, want %v", got, want)
	}
}

// TestSortByNameIsCaseInsensitive: "apple" must sort with the capitals, not
// after them as a raw byte comparison would put it.
func TestSortByNameIsCaseInsensitive(t *testing.T) {
	tbl := &taskTable{sortCol: 1, sortAsc: true}
	got := names(tbl.sorted(sample()))
	want := []string{"apple", "Banana", "Cherry"}
	if !equal(got, want) {
		t.Errorf("ascending by name: got %v, want %v", got, want)
	}

	tbl.sortAsc = false
	got = names(tbl.sorted(sample()))
	want = []string{"Cherry", "Banana", "apple"}
	if !equal(got, want) {
		t.Errorf("descending by name: got %v, want %v", got, want)
	}
}

// TestSortByColumnUsesThatColumn: sorting by duration and by start time yield
// different orders, proving each column drives its own comparison.
func TestSortByColumnUsesThatColumn(t *testing.T) {
	byDuration := (&taskTable{sortCol: 4, sortAsc: true}).sorted(sample())
	if got, want := names(byDuration), []string{"Cherry", "Banana", "apple"}; !equal(got, want) {
		t.Errorf("ascending by duration: got %v, want %v", got, want)
	}

	byStart := (&taskTable{sortCol: 2, sortAsc: true}).sorted(sample())
	if got, want := names(byStart), []string{"Cherry", "apple", "Banana"}; !equal(got, want) {
		t.Errorf("ascending by start: got %v, want %v", got, want)
	}
}

// TestSortDoesNotMutateInput: sorted() works on a copy, so the App's cached
// slice is never reordered underneath it.
func TestSortDoesNotMutateInput(t *testing.T) {
	in := sample()
	(&taskTable{sortCol: 1, sortAsc: true}).sorted(in)
	if got, want := names(in), []string{"Banana", "apple", "Cherry"}; !equal(got, want) {
		t.Errorf("input slice was reordered: got %v, want %v", got, want)
	}
}

// TestActionColumnIsNotSortable guards the one non-data column from being
// offered as a sort key.
func TestActionColumnIsNotSortable(t *testing.T) {
	action := len(tableColumns) - 1
	if sortableColumn(action) {
		t.Errorf("the Action column (index %d) must not be sortable", action)
	}
	if !sortableColumn(0) {
		t.Error("the # column should be sortable")
	}
}
