package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tsync "task-timer-app/internal/sync"
)

// newProvider builds a provider pointed at a test server, with the token in the
// environment exactly as the daemon would have it.
func newProvider(t *testing.T, baseURL string) *Provider {
	t.Helper()
	t.Setenv(defaultTokenEnv, "tt_test_token")

	cfg, err := json.Marshal(Config{
		BaseURL:             baseURL,
		APITokenEnv:         defaultTokenEnv,
		CompleteRemoteTasks: true,
	})
	if err != nil {
		t.Fatalf("marshalling config: %v", err)
	}

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Provider)
}

func TestNewRequiresABaseURL(t *testing.T) {
	t.Setenv(defaultTokenEnv, "tt_test_token")
	if _, err := New(json.RawMessage(`{}`)); err == nil {
		t.Fatal("a gateway with no base_url configured successfully; it would fail on the first cycle instead")
	}
}

// A daemon that starts without a token and only discovers it on the first cycle
// reports an opaque 401. Failing at construction says what to do about it.
func TestNewWithoutATokenSaysHowToGetOne(t *testing.T) {
	_, err := New(json.RawMessage(`{"base_url":"https://gw.example.com"}`))
	if err == nil {
		t.Fatal("configured with no token at all")
	}
	if !strings.Contains(err.Error(), "-connect") {
		t.Errorf("the error does not tell the user how to fix it: %v", err)
	}
}

func TestPullMapsTasks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tt_test_token" {
			t.Errorf("Authorization = %q, want the bearer token", got)
		}
		if got := r.URL.Path; got != "/api/v1/tasks" {
			t.Errorf("path = %q", got)
		}
		_, _ = w.Write([]byte(`{"tasks":[{
			"key":"ENG-1","title":"Do a thing","url":"https://acme.atlassian.net/browse/ENG-1",
			"status":"In Progress","assigned_by":"Dana","done":false,
			"updated_at":"2024-03-01T12:34:56Z"}]}`))
	}))
	defer srv.Close()

	remotes, err := newProvider(t, srv.URL).Pull(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(remotes) != 1 {
		t.Fatalf("got %d remotes, want 1", len(remotes))
	}
	if remotes[0].Key != "ENG-1" || remotes[0].AssignedBy != "Dana" || remotes[0].Done {
		t.Errorf("mapped badly: %+v", remotes[0])
	}
}

func TestPullSendsTheCursor(t *testing.T) {
	var since string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since = r.URL.Query().Get("since")
		_, _ = w.Write([]byte(`{"tasks":[]}`))
	}))
	defer srv.Close()

	when := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	if _, err := newProvider(t, srv.URL).Pull(context.Background(), when); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if since != "2024-03-01T09:00:00Z" {
		t.Errorf("since = %q, want the RFC 3339 cursor", since)
	}
}

func TestPushSendsTheSessionAndReturnsTheWorklogID(t *testing.T) {
	var body worklogRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		_, _ = w.Write([]byte(`{"jira_worklog_id":"10001","issue_key":"ENG-1","duplicate":false}`))
	}))
	defer srv.Close()

	start := time.Date(2024, 3, 1, 9, 15, 0, 0, time.UTC)
	got, err := newProvider(t, srv.URL).Push(context.Background(), tsync.WorkLog{
		Key:      "ENG-1",
		Started:  start,
		Duration: 25 * time.Minute,
		Comment:  "traced the drift",
		Author:   "bucky",
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got != "10001" {
		t.Errorf("Push returned %q, want the gateway's work-log id", got)
	}
	if body.IssueKey != "ENG-1" || body.DurationSeconds != 1500 {
		t.Errorf("sent %+v", body)
	}
	if body.IdempotencyKey == "" {
		t.Error("no idempotency key was sent; a retry would double-log to the tracker")
	}
}

// The engine re-pushes after a crash between the upstream write and the local
// mark. The remote tracker has no idempotency of its own, so the key that suppresses the
// duplicate has to be identical across those attempts — which means it must be
// derived from the session, not generated.
func TestIdempotencyKeyIsStableForTheSameSession(t *testing.T) {
	wl := tsync.WorkLog{
		Key:      "ENG-1",
		Started:  time.Date(2024, 3, 1, 9, 15, 0, 0, time.UTC),
		Duration: 25 * time.Minute,
		Author:   "bucky",
	}

	first := idempotencyKey(wl)
	if second := idempotencyKey(wl); first != second {
		t.Fatalf("the same session produced two keys (%s, %s); every retry would log again", first, second)
	}

	// A different session must not collide with it.
	other := wl
	other.Started = wl.Started.Add(time.Second)
	if idempotencyKey(other) == first {
		t.Error("two different sessions share an idempotency key; the second would be silently dropped")
	}
}

// The comment is optional and the gateway treats an absent one as "no comment".
func TestPushOmitsAnEmptyComment(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"jira_worklog_id":"1","issue_key":"ENG-1"}`))
	}))
	defer srv.Close()

	if _, err := newProvider(t, srv.URL).Push(context.Background(), tsync.WorkLog{
		Key: "ENG-1", Started: time.Now(), Duration: time.Minute,
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, present := raw["comment"]; present {
		t.Error("an empty comment was sent as a field rather than omitted")
	}
}

func TestCompleteIsOffUnlessEnabled(t *testing.T) {
	t.Setenv(defaultTokenEnv, "tt_test_token")

	p, err := New(json.RawMessage(`{"base_url":"https://gw.example.com"}`))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Closing an issue on a shared board is opt-in, so the default must be a skip
	// rather than a write.
	if err := p.Complete(context.Background(), "ENG-1"); err != tsync.ErrUnsupported {
		t.Errorf("Complete = %v, want ErrUnsupported when complete_remote_tasks is false", err)
	}
}

func TestCompleteCallsTheGateway(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issue_key":"ENG-1","transitioned":true}`))
	}))
	defer srv.Close()

	if err := newProvider(t, srv.URL).Complete(context.Background(), "ENG-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if path != "/api/v1/tasks/ENG-1/complete" {
		t.Errorf("path = %q", path)
	}
}

// A revoked token is the one failure a user can fix themselves, and the fix is a
// single command — so the error has to name it rather than say "401".
func TestA401TellsTheUserToReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"That API key is not valid."}`))
	}))
	defer srv.Close()

	_, err := newProvider(t, srv.URL).Pull(context.Background(), time.Time{})
	if err == nil {
		t.Fatal("a 401 was not reported as an error")
	}
	if !strings.Contains(err.Error(), "-connect") {
		t.Errorf("the 401 does not tell the user how to recover: %v", err)
	}
}

// The gateway's own explanation is the useful part of a failure; dropping it in
// favour of the status code throws away the diagnosis.
func TestTheGatewaysExplanationSurvivesIntoTheError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"detail":"the tracker refused the request: no permission on this board"}`))
	}))
	defer srv.Close()

	_, err := newProvider(t, srv.URL).Push(context.Background(), tsync.WorkLog{
		Key: "ENG-1", Started: time.Now(), Duration: time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "no permission on this board") {
		t.Errorf("lost the gateway's explanation: %v", err)
	}
}
