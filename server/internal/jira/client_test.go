package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Jira pedantry the Go provider learned the hard way. Every one of these is
// a rule Jira enforces and will reject a request over.

// --- started -----------------------------------------------------------------

func TestStartedHasThreeMillisAndColonlessOffset(t *testing.T) {
	when := time.Date(2024, 3, 1, 12, 34, 56, 789_123_000, time.UTC)
	if got := FormatStarted(when); got != "2024-03-01T12:34:56.789+0000" {
		t.Fatalf("got %q", got)
	}
}

func TestStartedPadsMillis(t *testing.T) {
	when := time.Date(2024, 3, 1, 12, 34, 56, 7_000_000, time.UTC)
	// .007, not .7 — Jira wants three digits, always.
	if got := FormatStarted(when); got != "2024-03-01T12:34:56.007+0000" {
		t.Fatalf("got %q", got)
	}
}

func TestStartedKeepsNonUTCOffsetColonless(t *testing.T) {
	when := time.Date(2024, 3, 1, 12, 0, 0, 0, time.FixedZone("", -5*3600))
	if got := FormatStarted(when); got != "2024-03-01T12:00:00.000-0500" {
		t.Fatalf("got %q", got)
	}
}

func TestStartedIsNotRFC3339(t *testing.T) {
	when := time.Date(2024, 3, 1, 12, 34, 56, 789_000_000, time.UTC)
	if FormatStarted(when) == when.Format(time.RFC3339Nano) {
		t.Fatal("format has regressed to RFC 3339, which Jira rejects")
	}
}

// --- the 60-second floor -----------------------------------------------------

func TestShortSessionsRoundUp(t *testing.T) {
	cases := []struct{ in, want int }{
		{40, 60}, {1, 60}, {59, 60}, {60, 60}, {61, 61}, {1500, 1500},
	}
	for _, c := range cases {
		if got := WorklogSeconds(c.in); got != c.want {
			t.Errorf("WorklogSeconds(%d)=%d, want %d", c.in, got, c.want)
		}
	}
}

// --- ADF ---------------------------------------------------------------------

func TestPlainADFShape(t *testing.T) {
	doc := PlainADF("traced the drift")
	if doc["type"] != "doc" || doc["version"] != 1 {
		t.Fatalf("bad doc envelope: %v", doc)
	}
	content := doc["content"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["text"] != "traced the drift" {
		t.Fatalf("bad text node: %v", content)
	}
}

func TestEmptyCommentOmitsTheField(t *testing.T) {
	var body map[string]any
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"10001"}`))
	})
	defer srv()

	c := NewClient(http.DefaultClient, "tok", "cloud-123", "")
	_, err := c.AddWorklog(context.Background(), WorkLog{
		IssueKey: "ENG-1", Started: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		DurationSeconds: 300, Comment: "   ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["comment"]; ok {
		t.Fatal("empty comment should omit the field entirely")
	}
}

func TestWorklogSendsTheJiraShape(t *testing.T) {
	var body map[string]any
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"99"}`))
	})
	defer srv()

	c := NewClient(http.DefaultClient, "tok", "cloud-123", "")
	got, err := c.AddWorklog(context.Background(), WorkLog{
		IssueKey: "ENG-412", Started: time.Date(2024, 3, 1, 9, 15, 0, 0, time.UTC),
		DurationSeconds: 30, Comment: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "99" {
		t.Fatalf("id = %q", got)
	}
	if body["started"] != "2024-03-01T09:15:00.000+0000" {
		t.Errorf("started = %v", body["started"])
	}
	if body["timeSpentSeconds"].(float64) != 60 { // rounded up off 30
		t.Errorf("timeSpentSeconds = %v", body["timeSpentSeconds"])
	}
	if body["comment"].(map[string]any)["type"] != "doc" {
		t.Errorf("comment is not ADF: %v", body["comment"])
	}
}

// --- done is a statusCategory, never a name ----------------------------------

func TestDoneComesFromStatusCategory(t *testing.T) {
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"isLast":true,"issues":[
          {"key":"ENG-1","fields":{"summary":"shipped",
            "status":{"name":"Released to prod","statusCategory":{"key":"done"}},
            "updated":"2024-03-01T12:34:56.789+0000","reporter":{"displayName":"Dana"}}},
          {"key":"ENG-2","fields":{
            "status":{"name":"Done pending review","statusCategory":{"key":"indeterminate"}}}}
        ]}`))
	})
	defer srv()

	tasks, err := NewClient(http.DefaultClient, "tok", "cloud-123", "https://acme.atlassian.net").
		Search(context.Background(), "x", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].Done != true || tasks[1].Done != false {
		t.Fatalf("done flags wrong: %+v", tasks)
	}
	if tasks[0].URL != "https://acme.atlassian.net/browse/ENG-1" {
		t.Errorf("url = %q", tasks[0].URL)
	}
	if tasks[0].AssignedBy != "Dana" {
		t.Errorf("assigned_by = %q", tasks[0].AssignedBy)
	}
}

// --- pagination --------------------------------------------------------------

func TestSearchFollowsNextPageToken(t *testing.T) {
	var n int
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Write([]byte(`{"issues":[{"key":"A-1","fields":{}}],"nextPageToken":"p2"}`))
		} else {
			w.Write([]byte(`{"issues":[{"key":"A-2","fields":{}}],"isLast":true}`))
		}
	})
	defer srv()

	tasks, err := NewClient(http.DefaultClient, "tok", "cloud-123", "").Search(context.Background(), "x", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].Key != "A-1" || tasks[1].Key != "A-2" {
		t.Fatalf("keys = %+v", tasks)
	}
}

func TestRepeatedCursorIsRefused(t *testing.T) {
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"issues":[{"key":"A-1","fields":{}}],"nextPageToken":"same"}`))
	})
	defer srv()

	_, err := NewClient(http.DefaultClient, "tok", "cloud-123", "").Search(context.Background(), "x", time.Time{})
	if err == nil || !strings.Contains(err.Error(), "same nextPageToken") {
		t.Fatalf("expected loop-guard error, got %v", err)
	}
}

// --- JQL ---------------------------------------------------------------------

func TestIncrementalJQLParenthesises(t *testing.T) {
	got := IncrementalJQL("assignee = currentUser() OR reporter = currentUser()",
		time.Date(2024, 3, 1, 9, 30, 0, 0, time.UTC))
	if !strings.HasPrefix(got, "(assignee = currentUser() OR reporter = currentUser()) AND updated >= ") {
		t.Fatalf("got %q", got)
	}
}

func TestIncrementalJQLWithoutCursorUntouched(t *testing.T) {
	if got := IncrementalJQL("assignee = currentUser()", time.Time{}); got != "assignee = currentUser()" {
		t.Fatalf("got %q", got)
	}
}

// --- timestamps in ------------------------------------------------------------

func TestParsesColonlessOffset(t *testing.T) {
	got, ok := ParseJiraTime("2024-03-01T12:34:56.789+0000")
	if !ok || !got.Equal(time.Date(2024, 3, 1, 12, 34, 56, 789_000_000, time.UTC)) {
		t.Fatalf("got %v ok=%v", got, ok)
	}
}

func TestUnparseableTimestampYieldsFalse(t *testing.T) {
	if _, ok := ParseJiraTime("last tuesday"); ok {
		t.Fatal("expected ok=false")
	}
}

// --- transitions --------------------------------------------------------------

func TestCompleteMatchesDestinationStatus(t *testing.T) {
	var posted map[string]any
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"transitions":[
              {"id":"11","name":"Start work","to":{"name":"In Progress"}},
              {"id":"31","name":"Finish work","to":{"name":"Done"}}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &posted)
		w.WriteHeader(204)
	})
	defer srv()

	if err := NewClient(http.DefaultClient, "tok", "cloud-123", "").
		Complete(context.Background(), "ENG-9", "Done"); err != nil {
		t.Fatal(err)
	}
	tr := posted["transition"].(map[string]any)
	if tr["id"] != "31" {
		t.Fatalf("transition id = %v, want 31", tr["id"])
	}
}

func TestMissingTransitionNamesExistingOnes(t *testing.T) {
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"transitions":[{"id":"11","name":"Start work","to":{"name":"In Progress"}}]}`))
	})
	defer srv()

	err := NewClient(http.DefaultClient, "tok", "cloud-123", "").Complete(context.Background(), "ENG-9", "Done")
	if err == nil || !strings.Contains(err.Error(), "Start work") {
		t.Fatalf("error should name available transitions, got %v", err)
	}
}

// --- errors -------------------------------------------------------------------

func TestJirasOwnComplaintSurvives(t *testing.T) {
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"errorMessages":[],"errors":{"timeSpentSeconds":"must be positive"}}`))
	})
	defer srv()

	_, err := NewClient(http.DefaultClient, "tok", "cloud-123", "").AddWorklog(context.Background(),
		WorkLog{IssueKey: "ENG-1", Started: time.Now().UTC(), DurationSeconds: 300})
	if err == nil || !strings.Contains(err.Error(), "timeSpentSeconds: must be positive") {
		t.Fatalf("got %v", err)
	}
}

func TestJQLStrippedFromErrors(t *testing.T) {
	srv := jiraStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("boom"))
	})
	defer srv()

	_, err := NewClient(http.DefaultClient, "tok", "cloud-123", "").
		Search(context.Background(), "assignee = 'alice@example.com'", time.Time{})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "alice@example.com") || strings.Contains(err.Error(), "?") {
		t.Fatalf("JQL leaked into error: %v", err)
	}
}

// jiraStub stands in for api.atlassian.com/ex/jira. It points APIBase at an
// httptest server and restores it on cleanup.
func jiraStub(t *testing.T, h http.HandlerFunc) func() {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := APIBase
	APIBase = srv.URL + "/ex/jira"
	return func() {
		APIBase = prev
		srv.Close()
	}
}
