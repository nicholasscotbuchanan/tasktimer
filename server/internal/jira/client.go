package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIBase is the 3LO Jira endpoint root; a call goes to <APIBase>/<cloud_id>/...
// It is a var so tests can point it at an httptest server.
var APIBase = "https://api.atlassian.com/ex/jira"

const (
	jiraTimeout = 30 * time.Second
	// pageSize is the search page. Asking for only the fields a listing needs
	// keeps the response small on a large board.
	pageSize = 100
	// pullFields is the minimum set a task listing needs.
	pullFields = "summary,status,assignee,updated,reporter"
	// minWorklogSeconds is Jira's floor for a work log.
	minWorklogSeconds = 60
	maxErrBody        = 512
)

// Error means Jira refused a request. The message carries Jira's own explanation
// and StatusCode carries the code, so callers can map it (a 401/403 from Jira is
// not the client's fault and must not be reported as one).
type Error struct {
	Message    string
	StatusCode int
}

func (e *Error) Error() string { return e.Message }

// RemoteTask is a Jira issue as the desktop client's task list wants it.
type RemoteTask struct {
	Key        string
	Title      string
	URL        string
	Status     string
	AssignedBy string
	Done       bool
	UpdatedAt  time.Time // zero when Jira's value was absent or unparseable
}

// WorkLog is a completed local timer session on its way to Jira.
type WorkLog struct {
	IssueKey        string
	Started         time.Time
	DurationSeconds int
	Comment         string
}

// FormatStarted renders a work log's `started` the one way Jira accepts:
// exactly three fractional digits and an offset with NO colon. RFC 3339 is
// rejected outright, which is why time.RFC3339 was unusable in the Go provider
// this ports, too.
func FormatStarted(when time.Time) string {
	millis := when.Nanosecond() / 1_000_000
	return fmt.Sprintf("%s.%03d%s", when.Format("2006-01-02T15:04:05"), millis, when.Format("-0700"))
}

// WorklogSeconds returns whole seconds, rounded up to Jira's one-minute floor.
func WorklogSeconds(seconds int) int {
	if seconds < minWorklogSeconds {
		return minWorklogSeconds
	}
	return seconds
}

// PlainADF wraps one line of text as a single-paragraph ADF document.
func PlainADF(text string) map[string]any {
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		},
	}
}

// ParseJiraTime parses Jira's colon-less-offset timestamps. An unparseable value
// yields ok=false rather than failing a whole listing: one odd `updated` field
// is not a reason to drop ninety-nine good issues.
func ParseJiraTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999-0700",
		"2006-01-02T15:04:05-0700",
		time.RFC3339,
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// IncrementalJQL narrows the configured query to issues touched since the last
// look. The user's JQL is parenthesised so a top-level OR inside it cannot
// swallow the added clause. Jira compares `updated` at minute precision only, so
// the cursor is deliberately coarse — a little overlap is harmless.
func IncrementalJQL(jql string, since time.Time) string {
	if since.IsZero() {
		return jql
	}
	return fmt.Sprintf("(%s) AND updated >= %q", jql, since.Local().Format("2006/01/02 15:04"))
}

// Client talks to one Jira site as one user.
type Client struct {
	http    *http.Client
	token   string
	cloudID string
	siteURL string
}

// NewClient builds a per-user Jira client.
func NewClient(httpc *http.Client, accessToken, cloudID, siteURL string) *Client {
	return &Client{
		http:    httpc,
		token:   accessToken,
		cloudID: cloudID,
		siteURL: strings.TrimRight(siteURL, "/"),
	}
}

// Search returns every issue matching the JQL, following Jira's cursor
// pagination. /search/jql is token-paginated: there is no startAt, and the end
// is marked by isLast or by the absence of a nextPageToken.
func (c *Client) Search(ctx context.Context, jql string, since time.Time) ([]RemoteTask, error) {
	query := IncrementalJQL(jql, since)
	var tasks []RemoteTask
	token := ""
	seen := map[string]bool{}

	for {
		params := url.Values{}
		params.Set("jql", query)
		params.Set("fields", pullFields)
		params.Set("maxResults", fmt.Sprint(pageSize))
		if token != "" {
			params.Set("nextPageToken", token)
		}

		var page struct {
			Issues        []json.RawMessage `json:"issues"`
			NextPageToken string            `json:"nextPageToken"`
			IsLast        bool              `json:"isLast"`
		}
		if err := c.request(ctx, http.MethodGet, "/rest/api/3/search/jql?"+params.Encode(), nil, &page); err != nil {
			return nil, err
		}

		for _, raw := range page.Issues {
			tasks = append(tasks, c.toRemote(raw))
		}

		if page.IsLast || page.NextPageToken == "" {
			return tasks, nil
		}
		// A server that kept handing back the same cursor would spin here forever,
		// hammering Jira and growing the list until we were killed.
		if seen[page.NextPageToken] {
			return nil, &Error{Message: fmt.Sprintf(
				"Jira returned the same nextPageToken %q twice; giving up rather than paging forever.",
				page.NextPageToken)}
		}
		seen[page.NextPageToken] = true
		token = page.NextPageToken
	}
}

func (c *Client) toRemote(raw json.RawMessage) RemoteTask {
	var issue struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
			Updated string `json:"updated"`
			Status  struct {
				Name           string `json:"name"`
				StatusCategory struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"status"`
			Reporter struct {
				DisplayName string `json:"displayName"`
			} `json:"reporter"`
		} `json:"fields"`
	}
	_ = json.Unmarshal(raw, &issue)

	url := ""
	if c.siteURL != "" {
		url = c.siteURL + "/browse/" + issue.Key
	}
	updated, _ := ParseJiraTime(issue.Fields.Updated)
	return RemoteTask{
		Key:        issue.Key,
		Title:      issue.Fields.Summary,
		URL:        url,
		Status:     issue.Fields.Status.Name,
		AssignedBy: issue.Fields.Reporter.DisplayName,
		// The coarse category, never the status name: only this is comparable
		// across projects with different workflows.
		Done:      strings.EqualFold(issue.Fields.Status.StatusCategory.Key, "done"),
		UpdatedAt: updated,
	}
}

// AddWorklog records a session on the issue; returns Jira's id for the work log.
func (c *Client) AddWorklog(ctx context.Context, wl WorkLog) (string, error) {
	if strings.TrimSpace(wl.IssueKey) == "" {
		return "", &Error{Message: "cannot push a work log without an issue key"}
	}

	body := map[string]any{
		"started":          FormatStarted(wl.Started),
		"timeSpentSeconds": WorklogSeconds(wl.DurationSeconds),
	}
	// An ADF document with an empty text node is rejected, so an empty comment
	// means no comment field at all rather than an empty one.
	if comment := strings.TrimSpace(wl.Comment); comment != "" {
		body["comment"] = PlainADF(comment)
	}

	var created struct {
		ID string `json:"id"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(wl.IssueKey) + "/worklog"
	if err := c.request(ctx, http.MethodPost, path, body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", &Error{Message: fmt.Sprintf(
			"Jira accepted the work log on %s but returned no work log id", wl.IssueKey)}
	}
	return created.ID, nil
}

type transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
}

// Complete drives the issue through its done transition.
func (c *Client) Complete(ctx context.Context, issueKey, doneTransition string) error {
	if strings.TrimSpace(issueKey) == "" {
		return &Error{Message: "cannot complete an issue without a key"}
	}

	path := "/rest/api/3/issue/" + url.PathEscape(issueKey) + "/transitions"
	var avail struct {
		Transitions []transition `json:"transitions"`
	}
	if err := c.request(ctx, http.MethodGet, path, nil, &avail); err != nil {
		return err
	}

	id := findTransition(avail.Transitions, doneTransition)
	if id == "" {
		return &Error{Message: fmt.Sprintf(
			"No transition matching %q is available on %s. Available transitions: %s",
			doneTransition, issueKey, describeTransitions(avail.Transitions))}
	}

	body := map[string]any{"transition": map[string]any{"id": id}}
	return c.request(ctx, http.MethodPost, path, body, nil)
}

// request executes one call against api.atlassian.com/ex/jira/<cloud_id>.
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	endpoint := fmt.Sprintf("%s/%s%s", APIBase, c.cloudID, path)

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return &Error{Message: fmt.Sprintf("encoding request: %v", err)}
		}
		reader = strings.NewReader(string(encoded))
	}

	ctx, cancel := context.WithTimeout(ctx, jiraTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return &Error{Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Message: fmt.Sprintf("%s %s: %v", method, redact(endpoint), err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(method, endpoint, resp)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &Error{Message: fmt.Sprintf("reading response: %v", err)}
	}
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// A 2xx with a body that is not the shape we expected is not fatal for the
		// no-output callers; for output callers it is a genuine surprise.
		return &Error{Message: fmt.Sprintf("decoding response from %s: %v", redact(endpoint), err)}
	}
	return nil
}

// findTransition matches on the transition's own name, or on the status it leads
// to. Boards vary: some name the transition "Done", others name it "Finish work"
// and only the destination status is "Done". Accepting either spares the user
// from having to know which kind of board they are on.
func findTransition(transitions []transition, want string) string {
	target := strings.ToLower(strings.TrimSpace(want))
	for _, t := range transitions {
		name := strings.ToLower(strings.TrimSpace(t.Name))
		to := strings.ToLower(strings.TrimSpace(t.To.Name))
		if target == name || target == to {
			return t.ID
		}
	}
	return ""
}

// describeTransitions renders the available transitions for an error message. A
// missing transition is the single most common Jira misconfiguration there is,
// and the fix is always "use one of these names" — so the names go in the error.
func describeTransitions(transitions []transition) string {
	if len(transitions) == 0 {
		return "(none — the issue may already be closed, or the account may lack permission to transition it)"
	}
	parts := make([]string, 0, len(transitions))
	for _, t := range transitions {
		if t.To.Name != "" && !strings.EqualFold(t.To.Name, t.Name) {
			parts = append(parts, fmt.Sprintf("%q (to %q)", t.Name, t.To.Name))
		} else {
			parts = append(parts, fmt.Sprintf("%q", t.Name))
		}
	}
	return strings.Join(parts, ", ")
}

// apiError turns a non-2xx into an error that says what Jira actually objected
// to. Jira answers a bad work log with a 400 whose body explains exactly why;
// discarding that in favour of "unexpected status 400" throws away the only
// useful part of the response.
func apiError(method, endpoint string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	detail := ""
	var body struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(raw, &body) == nil && (len(body.ErrorMessages) > 0 || len(body.Errors) > 0) {
		parts := append([]string{}, body.ErrorMessages...)
		for field, msg := range body.Errors {
			parts = append(parts, fmt.Sprintf("%s: %s", field, msg))
		}
		detail = strings.Join(parts, "; ")
	} else {
		detail = strings.TrimSpace(string(raw))
	}
	if len(detail) > maxErrBody {
		detail = detail[:maxErrBody]
	}

	msg := fmt.Sprintf("%s %s: Jira returned %d", method, redact(endpoint), resp.StatusCode)
	if detail != "" {
		msg = fmt.Sprintf("%s: %s", msg, detail)
	}
	return &Error{Message: msg, StatusCode: resp.StatusCode}
}

// redact strips the query string before a URL goes into an error or a log line.
// The JQL in a search URL is long, noisy, and can name people.
func redact(endpoint string) string {
	if i := strings.IndexByte(endpoint, '?'); i >= 0 {
		return endpoint[:i]
	}
	return endpoint
}
