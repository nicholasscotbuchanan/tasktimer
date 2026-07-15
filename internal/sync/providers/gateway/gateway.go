// Package gateway talks to the Task Timer backend, which is the single gateway
// through which the remote task tracker is reached.
//
// The client holds no tracker credential of any kind. It authenticates to the
// gateway with a bearer token, and the backend — which holds that user's own
// grant and is the only thing that integrates with the tracker — performs the
// write on their behalf, so the work log is authored in the tracker by the
// person who did the work rather than by a shared robot.
//
// The provider registers itself as "gateway"; a binary that wants it compiled in
// blank-imports this package.
package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	tsync "task-timer-app/internal/sync"
	"task-timer-app/internal/task"
)

// ProviderName is the name this provider registers under. It is persisted in the
// task_source column, so it must not change.
const ProviderName = "gateway"

const (
	// defaultTokenEnv is the environment variable the bearer token is read from.
	// It lives in the daemon's sync.env, not in sync.json: a token in a config
	// file is a token in a backup, a screen share, and a support ticket.
	defaultTokenEnv = "TASK_TIMER_GATEWAY_TOKEN"
	// defaultTimeout bounds a single HTTP request.
	defaultTimeout = 30 * time.Second
	// maxErrBody caps how much of a gateway error response is quoted back.
	maxErrBody = 512
)

// Config is the provider's block of the sync config file.
type Config struct {
	// BaseURL is the gateway's root, e.g. "https://tasktimer.corp.example.com".
	BaseURL string `json:"base_url"`
	// APITokenEnv names an environment variable holding the bearer token. The
	// Connect flow writes it to sync.env under this name.
	APITokenEnv string `json:"api_token_env"`
	// APIToken is an inline token, for anyone who hand-edits the config. Prefer
	// APITokenEnv.
	APIToken string `json:"api_token"`
	// CompleteRemoteTasks enables Complete. It defaults to false: closing an issue
	// on a shared board is opt-in, and the gateway independently refuses it unless
	// its own server-side allow_complete is set.
	CompleteRemoteTasks bool `json:"complete_remote_tasks"`
}

// Provider is the Task Timer gateway backend.
type Provider struct {
	cfg    Config
	token  string
	client *http.Client
}

var _ tsync.Provider = (*Provider)(nil)

func init() {
	tsync.Register(tsync.Registration{
		Name:     ProviderName,
		Title:    "Task Timer Gateway",
		Summary:  "Reach your task tracker through the Task Timer backend; only a bearer token is held here.",
		New:      New,
		Fields:   Fields(),
		URLField: "base_url",
		Connect:  connect,
		HasToken: hasToken,
	})
}

// Fields declares the settings a user may edit, so the desktop app can render a
// form for this backend without importing it.
//
// The token itself is deliberately absent: only the
// *name* of the environment variable holding it is offered on screen. A token in
// a text field is a token in a screenshot.
func Fields() []tsync.Field {
	return []tsync.Field{
		{
			Key:         "base_url",
			Label:       "Gateway URL",
			Hint:        "The Task Timer backend this client synchronises through.",
			Kind:        tsync.KindText,
			Placeholder: "https://tasktimer.example.com",
			Default:     "",
		},
		{
			Key:         "api_token_env",
			Label:       "Token variable",
			Hint:        "The variable holding the bearer token; Log in writes it.",
			Kind:        tsync.KindText,
			Placeholder: defaultTokenEnv,
			Default:     defaultTokenEnv,
		},
		{
			Key:     "complete_remote_tasks",
			Label:   "Completion",
			Hint:    "Let Complete close the remote issue. The gateway must also allow it.",
			Kind:    tsync.KindBool,
			Default: false,
		},
	}
}

// New is the sync.Factory for the gateway. It validates the config and resolves
// the token up front, so a misconfigured provider fails at daemon start with a
// clear message rather than on the first sync cycle with an opaque 401.
func New(raw json.RawMessage) (tsync.Provider, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parsing gateway settings: %w", err)
		}
	}

	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New(`gateway: "base_url" is required (e.g. "https://tasktimer.example.com")`)
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("gateway: %q is not a valid base_url", cfg.BaseURL)
	}

	token, err := resolveToken(cfg)
	if err != nil {
		return nil, err
	}

	return &Provider{
		cfg:    cfg,
		token:  token,
		client: &http.Client{Timeout: defaultTimeout},
	}, nil
}

// resolveToken prefers the environment variable over an inline token. A named
// variable that is not set is an error rather than a silent fallback: the user
// asked for that variable, and quietly authenticating as somebody else — or as
// nobody — would be worse than not starting.
func resolveToken(cfg Config) (string, error) {
	name := strings.TrimSpace(cfg.APITokenEnv)
	if name == "" && cfg.APIToken == "" {
		name = defaultTokenEnv
	}

	if name != "" {
		token, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(token) == "" {
			if cfg.APIToken != "" {
				return cfg.APIToken, nil
			}
			return "", fmt.Errorf(
				"gateway: no bearer token. %s is not set. Run 'task-timer-sync -connect' "+
					"to sign in to the gateway; it writes the token to the daemon's sync.env.",
				name)
		}
		return token, nil
	}
	return cfg.APIToken, nil
}

// Name implements sync.Provider.
func (p *Provider) Name() string { return ProviderName }

// ---------------------------------------------------------------------------
// Pull
// ---------------------------------------------------------------------------

type taskList struct {
	Tasks []remoteTask `json:"tasks"`
}

type remoteTask struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	AssignedBy string `json:"assigned_by"`
	Done       bool   `json:"done"`
	UpdatedAt  string `json:"updated_at"`
}

// Pull returns the tasks the gateway reports as assigned to this user. The
// gateway owns the query that selects them; this client does not get to ask for
// arbitrary issues, which is a smaller surface than a timer needs anyway.
func (p *Provider) Pull(ctx context.Context, since time.Time) ([]task.Remote, error) {
	endpoint := p.cfg.BaseURL + "/api/v1/tasks"
	if !since.IsZero() {
		q := url.Values{}
		q.Set("since", since.UTC().Format(time.RFC3339))
		endpoint += "?" + q.Encode()
	}

	var body taskList
	if err := p.do(ctx, http.MethodGet, endpoint, nil, &body); err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}

	remotes := make([]task.Remote, 0, len(body.Tasks))
	for _, t := range body.Tasks {
		remotes = append(remotes, task.Remote{
			Key:        t.Key,
			Title:      t.Title,
			URL:        t.URL,
			Status:     t.Status,
			AssignedBy: t.AssignedBy,
			Done:       t.Done,
			UpdatedAt:  parseTime(t.UpdatedAt),
		})
	}
	return remotes, nil
}

// parseTime accepts the gateway's RFC 3339 timestamps. An unparseable value
// yields the zero time rather than failing the whole pull: one odd `updated_at`
// is not a reason to drop a hundred good tasks.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// Push
// ---------------------------------------------------------------------------

type worklogRequest struct {
	IssueKey        string `json:"issue_key"`
	Started         string `json:"started"`
	DurationSeconds int64  `json:"duration_seconds"`
	Comment         string `json:"comment,omitempty"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type worklogResponse struct {
	WorklogID string `json:"jira_worklog_id"`
	IssueKey  string `json:"issue_key"`
	Duplicate bool   `json:"duplicate"`
}

// Push sends a completed session to the gateway and returns the upstream work-log
// id, which the engine persists as the session's sync signature.
func (p *Provider) Push(ctx context.Context, wl tsync.WorkLog) (string, error) {
	if strings.TrimSpace(wl.Key) == "" {
		return "", errors.New("gateway: cannot push a work log without a task key")
	}

	body := worklogRequest{
		IssueKey:        wl.Key,
		Started:         wl.Started.Format(time.RFC3339Nano),
		DurationSeconds: int64(wl.Duration.Round(time.Second) / time.Second),
		Comment:         wl.Comment,
		IdempotencyKey:  idempotencyKey(wl),
	}

	var created worklogResponse
	endpoint := p.cfg.BaseURL + "/api/v1/worklogs"
	if err := p.do(ctx, http.MethodPost, endpoint, body, &created); err != nil {
		return "", fmt.Errorf("pushing work log for %s: %w", wl.Key, err)
	}
	if created.WorklogID == "" {
		return "", fmt.Errorf("gateway accepted the work log on %s but returned no work-log id", wl.Key)
	}
	return created.WorklogID, nil
}

// idempotencyKey derives a stable id for a session from the session itself.
//
// The engine re-pushes after a crash that lands between the upstream write and
// the local mark — that is by design, and providers are expected to tolerate it.
// The remote tracker will not: it accepts the same work log as many times as it is sent, and
// there is no way to take the duplicates back except by hand. So the key has to
// be identical across those retries, which means deriving it from the row's own
// content rather than generating it fresh.
//
// Start time carries nanosecond precision, so two distinct sessions colliding
// would require the same task to be started twice at the same instant for the
// same duration.
func idempotencyKey(wl tsync.WorkLog) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00%s",
		wl.Key, wl.Started.UTC().UnixNano(), int64(wl.Duration), wl.Author)
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Complete
// ---------------------------------------------------------------------------

// Complete asks the gateway to close the remote task.
func (p *Provider) Complete(ctx context.Context, key string) error {
	if !p.cfg.CompleteRemoteTasks {
		return tsync.ErrUnsupported
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("gateway: cannot complete a task without a key")
	}

	endpoint := fmt.Sprintf("%s/api/v1/tasks/%s/complete", p.cfg.BaseURL, url.PathEscape(key))
	if err := p.do(ctx, http.MethodPost, endpoint, nil, nil); err != nil {
		return fmt.Errorf("completing %s: %w", key, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

// do executes one authenticated request against the gateway, decoding a 2xx into
// out (which may be nil to discard it) and turning anything else into an error
// carrying the gateway's own explanation.
func (p *Provider) do(ctx context.Context, method, endpoint string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("building %s request: %w", method, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, redact(endpoint), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(method, endpoint, resp)
	}

	if out == nil {
		// Drain so the connection can be reused rather than torn down.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s %s: %w", method, redact(endpoint), err)
	}
	return nil
}

// apiError turns a non-2xx into an error that says what the gateway objected to.
//
// A 401 is called out by name. It is the one failure the user can act on without
// reading a log — their token has been revoked or was never written — and the fix
// is a single command, so the error says which one.
func apiError(method, endpoint string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var problem struct {
		Detail string `json:"detail"`
	}
	detail := ""
	if err := json.Unmarshal(raw, &problem); err == nil && problem.Detail != "" {
		detail = truncate(problem.Detail, maxErrBody)
	} else {
		detail = truncate(strings.TrimSpace(string(raw)), maxErrBody)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf(
			"the gateway rejected this client's token (401). Run 'task-timer-sync -connect' "+
				"to sign in again. %s", detail)
	}

	if detail != "" {
		return fmt.Errorf("%s %s: gateway returned %s: %s", method, redact(endpoint), resp.Status, detail)
	}
	return fmt.Errorf("%s %s: gateway returned %s", method, redact(endpoint), resp.Status)
}

// redact strips the query string from a URL before it goes into an error.
func redact(endpoint string) string {
	if i := strings.IndexByte(endpoint, '?'); i >= 0 {
		return endpoint[:i]
	}
	return endpoint
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
