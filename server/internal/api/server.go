// Package api is the gateway's HTTP surface.
//
// The routes here are the contract the desktop client already speaks (see
// internal/sync/providers/gateway in the client module). There is one auth flow
// that registers, authenticates, and connects Jira all at once, and three
// working endpoints behind a bearer key: list tasks, complete a task, push a
// work log.
package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"task-timer-server/internal/config"
	"task-timer-server/internal/crypto"
	"task-timer-server/internal/jira"
	"task-timer-server/internal/store"
)

// Time limits on the two short-lived rows of a login.
const (
	// pendingTTL: long enough to read a consent screen and think, short enough
	// that an abandoned login does not leave a redeemable row lying around.
	pendingTTL = 10 * time.Minute
	// codeTTL: the client is already listening when the browser redirects, so
	// this only has to survive the hop from browser to loopback to exchange call.
	codeTTL = 2 * time.Minute
)

// Server holds the gateway's dependencies.
type Server struct {
	store  *store.Store
	cfg    config.Settings
	cipher *crypto.Cipher
	http   *http.Client
}

// New builds a Server. The HTTP client is shared across every outbound call
// (Atlassian OAuth and Jira alike); rebuilding it per request would be a
// self-inflicted TLS-handshake tax on every push.
func New(st *store.Store, cfg config.Settings, cipher *crypto.Cipher) *Server {
	return &Server{
		store:  st,
		cfg:    cfg,
		cipher: cipher,
		http: &http.Client{
			// An unexpected 3xx from the token endpoint or Jira should surface as a
			// visible non-2xx, not be quietly followed.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Handler wires the routes. Go 1.22 method+path patterns give us the routing the
// old FastAPI app had without a framework.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /api/v1/auth/exchange", s.exchange)

	mux.HandleFunc("GET /api/v1/me", s.authed(s.me))
	mux.HandleFunc("GET /api/v1/tasks", s.authed(s.listTasks))
	mux.HandleFunc("POST /api/v1/tasks/{issue_key}/complete", s.authed(s.completeTask))
	mux.HandleFunc("POST /api/v1/worklogs", s.authed(s.pushWorklog))

	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Auth flow
// ---------------------------------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.RequireOAuth(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	q := r.URL.Query()
	redirectURI := q.Get("redirect_uri")
	if !isLoopback(redirectURI) {
		writeError(w, http.StatusBadRequest,
			"redirect_uri must be a loopback address, e.g. http://127.0.0.1:53682/callback")
		return
	}

	ourState, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start the login")
		return
	}
	if err := s.store.CreatePendingAuth(store.PendingAuth{
		State:             ourState,
		CodeChallenge:     q.Get("code_challenge"),
		ClientRedirectURI: redirectURI,
		ClientState:       q.Get("state"),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "could not start the login")
		return
	}

	http.Redirect(w, r,
		jira.AuthorizeURLFor(s.cfg.AtlassianClientID, s.cfg.RedirectURI(), ourState),
		http.StatusTemporaryRedirect)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		desc := q.Get("error_description")
		if desc == "" {
			desc = e
		}
		writePage(w, "Jira connection cancelled", desc, false)
		return
	}

	pending, ok, err := s.store.TakePendingAuth(q.Get("state"))
	if err != nil {
		writePage(w, "Could not connect to Jira", "Please try again.", false)
		return
	}
	if !ok || expiredSince(pending.CreatedAt, pendingTTL) {
		// Either a replay, or a login left open past its TTL and finished later.
		writePage(w, "That login has expired",
			"Close this tab and press 'Connect to Jira' in Task Timer again.", false)
		return
	}

	ctx := r.Context()
	tokens, err := jira.ExchangeCode(ctx, s.http,
		s.cfg.AtlassianClientID, s.cfg.AtlassianClientSecret, q.Get("code"), s.cfg.RedirectURI())
	if err != nil {
		writePage(w, "Could not connect to Jira", err.Error(), false)
		return
	}
	who, err := jira.WhoAmI(ctx, s.http, tokens.AccessToken)
	if err != nil {
		writePage(w, "Could not connect to Jira", err.Error(), false)
		return
	}
	site, err := jira.FirstJiraSite(ctx, s.http, tokens.AccessToken)
	if err != nil {
		writePage(w, "Could not connect to Jira", err.Error(), false)
		return
	}

	if !s.cfg.DomainAllowed(who.Email) {
		name := who.Email
		if name == "" {
			name = "That account"
		}
		writePage(w, "This account cannot register",
			name+" is not in an allowed domain for this server.", false)
		return
	}

	user, err := s.upsertUser(who, tokens, site)
	if err != nil {
		writePage(w, "Could not connect to Jira", "Please try again.", false)
		return
	}

	code, err := randomToken(32)
	if err != nil {
		writePage(w, "Could not connect to Jira", "Please try again.", false)
		return
	}
	if err := s.store.CreateAuthCode(store.AuthCode{
		Code:          code,
		UserID:        user.ID,
		CodeChallenge: pending.CodeChallenge,
		ExpiresAt:     time.Now().UTC().Add(codeTTL),
	}); err != nil {
		writePage(w, "Could not connect to Jira", "Please try again.", false)
		return
	}

	// The bearer key is NOT put in this redirect. A URL lands in browser history,
	// in a proxy log, and in the Referer of whatever the page loads next. The
	// client trades this short-lived code for the key over TLS.
	sep := "?"
	if strings.Contains(pending.ClientRedirectURI, "?") {
		sep = "&"
	}
	dest := pending.ClientRedirectURI + sep + url.Values{
		"code":  {code},
		"state": {pending.ClientState},
	}.Encode()
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// upsertUser finds the user, or registers them: first consent creates the
// account. It also (re)writes the Jira grant.
func (s *Server) upsertUser(who jira.Identity, tokens jira.TokenSet, site jira.Site) (store.User, error) {
	user, ok, err := s.store.UserByAtlassianID(who.AccountID)
	if err != nil {
		return store.User{}, err
	}
	if !ok {
		user, err = s.store.CreateUser(who.AccountID, who.Email, who.DisplayName)
		if err != nil {
			return store.User{}, err
		}
	} else {
		// People change their name and their email; the account id never changes.
		email := who.Email
		if email == "" {
			email = user.Email
		}
		name := who.DisplayName
		if name == "" {
			name = user.DisplayName
		}
		if err := s.store.UpdateUserProfile(user.ID, email, name); err != nil {
			return store.User{}, err
		}
		user.Email, user.DisplayName = email, name
	}

	if err := s.storeTokens(user.ID, tokens, site.CloudID, site.URL); err != nil {
		return store.User{}, err
	}
	return user, nil
}

// storeTokens encrypts and persists a grant. Every refresh routes through here,
// because Atlassian kills the old refresh token the instant it issues a new one.
func (s *Server) storeTokens(userID int64, t jira.TokenSet, cloudID, siteURL string) error {
	access, err := s.cipher.Encrypt(t.AccessToken)
	if err != nil {
		return err
	}
	refresh, err := s.cipher.Encrypt(t.RefreshToken)
	if err != nil {
		return err
	}
	return s.store.UpsertJiraToken(store.JiraToken{
		UserID:          userID,
		AccessTokenEnc:  access,
		RefreshTokenEnc: refresh,
		ExpiresAt:       t.ExpiresAt,
		CloudID:         cloudID,
		SiteURL:         siteURL,
	})
}

type exchangeRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

type meResponse struct {
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	JiraConnected bool   `json:"jira_connected"`
	JiraSiteURL   string `json:"jira_site_url"`
}

type exchangeResponse struct {
	APIKey string     `json:"api_key"`
	User   meResponse `json:"user"`
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	var body exchangeRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "That code is not valid.")
		return
	}

	// One shot, valid or not. Taking (and deleting) the code before the checks
	// below means a wrong verifier burns it rather than letting it be brute-forced.
	record, ok, err := s.store.TakeAuthCode(body.Code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not complete the exchange.")
		return
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "That code is not valid.")
		return
	}
	if expiredAt(record.ExpiresAt) {
		writeError(w, http.StatusBadRequest, "That code has expired. Connect again.")
		return
	}
	if !crypto.PKCEVerify(body.CodeVerifier, record.CodeChallenge) {
		writeError(w, http.StatusBadRequest, "The PKCE verifier does not match.")
		return
	}

	user, ok, err := s.store.UserByID(record.UserID)
	if err != nil || !ok {
		writeError(w, http.StatusBadRequest, "That code is not valid.")
		return
	}

	raw, hash, prefix, err := crypto.NewAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not issue a key.")
		return
	}
	if err := s.store.CreateAPIKey(user.ID, hash, prefix, "desktop"); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not issue a key.")
		return
	}

	me, err := s.meFor(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the account.")
		return
	}
	writeJSON(w, http.StatusOK, exchangeResponse{APIKey: raw, User: me})
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, user store.User) {
	me, err := s.meFor(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the account.")
		return
	}
	writeJSON(w, http.StatusOK, me)
}

func (s *Server) meFor(user store.User) (meResponse, error) {
	token, ok, err := s.store.GetJiraToken(user.ID)
	if err != nil {
		return meResponse{}, err
	}
	return meResponse{
		Email:         user.Email,
		DisplayName:   user.DisplayName,
		JiraConnected: ok,
		JiraSiteURL:   token.SiteURL,
	}, nil
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

type taskJSON struct {
	Key        string  `json:"key"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Status     string  `json:"status"`
	AssignedBy string  `json:"assigned_by"`
	Done       bool    `json:"done"`
	UpdatedAt  *string `json:"updated_at"`
}

type taskListJSON struct {
	Tasks []taskJSON `json:"tasks"`
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, user store.User) {
	var since time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			since = t
		}
	}

	client, herr := s.jiraFor(r.Context(), user)
	if herr != nil {
		herr.write(w)
		return
	}

	remotes, err := client.Search(r.Context(), s.cfg.JiraJQL, since)
	if err != nil {
		upstream(err).write(w)
		return
	}

	tasks := make([]taskJSON, 0, len(remotes))
	for _, rt := range remotes {
		t := taskJSON{
			Key:        rt.Key,
			Title:      rt.Title,
			URL:        rt.URL,
			Status:     rt.Status,
			AssignedBy: rt.AssignedBy,
			Done:       rt.Done,
		}
		if !rt.UpdatedAt.IsZero() {
			s := rt.UpdatedAt.UTC().Format(time.RFC3339Nano)
			t.UpdatedAt = &s
		}
		tasks = append(tasks, t)
	}
	writeJSON(w, http.StatusOK, taskListJSON{Tasks: tasks})
}

type completeResponse struct {
	IssueKey     string `json:"issue_key"`
	Transitioned bool   `json:"transitioned"`
}

func (s *Server) completeTask(w http.ResponseWriter, r *http.Request, user store.User) {
	if !s.cfg.JiraAllowComplete {
		writeError(w, http.StatusForbidden,
			"This server does not allow clients to close Jira issues. "+
				"An administrator can enable it with jira.allow_complete.")
		return
	}
	issueKey := r.PathValue("issue_key")

	client, herr := s.jiraFor(r.Context(), user)
	if herr != nil {
		herr.write(w)
		return
	}
	if err := client.Complete(r.Context(), issueKey, s.cfg.JiraDoneTransition); err != nil {
		upstream(err).write(w)
		return
	}
	writeJSON(w, http.StatusOK, completeResponse{IssueKey: issueKey, Transitioned: true})
}

// ---------------------------------------------------------------------------
// Work logs
// ---------------------------------------------------------------------------

type worklogRequest struct {
	IssueKey        string `json:"issue_key"`
	Started         string `json:"started"`
	DurationSeconds int    `json:"duration_seconds"`
	Comment         string `json:"comment"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type worklogResponse struct {
	JiraWorklogID string `json:"jira_worklog_id"`
	IssueKey      string `json:"issue_key"`
	Duplicate     bool   `json:"duplicate"`
}

func (s *Server) pushWorklog(w http.ResponseWriter, r *http.Request, user store.User) {
	var body worklogRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "The work log request could not be read.")
		return
	}
	if body.DurationSeconds <= 0 {
		writeError(w, http.StatusUnprocessableEntity, "duration_seconds must be greater than zero.")
		return
	}
	if n := len(body.IdempotencyKey); n < 8 || n > 128 {
		writeError(w, http.StatusUnprocessableEntity, "idempotency_key must be between 8 and 128 characters.")
		return
	}
	started, err := time.Parse(time.RFC3339, body.Started)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "started must be an RFC 3339 timestamp with an offset.")
		return
	}

	// Idempotency: a repeat returns the first push's id and touches Jira not at
	// all. The client retries on its own schedule, so this WILL be called twice.
	if seen, ok, err := s.store.PushedWorklogByKey(user.ID, body.IdempotencyKey); err == nil && ok {
		writeJSON(w, http.StatusCreated, worklogResponse{
			JiraWorklogID: seen.JiraWorklogID, IssueKey: seen.IssueKey, Duplicate: true,
		})
		return
	}

	client, herr := s.jiraFor(r.Context(), user)
	if herr != nil {
		herr.write(w)
		return
	}

	worklogID, err := client.AddWorklog(r.Context(), jira.WorkLog{
		IssueKey:        body.IssueKey,
		Started:         started,
		DurationSeconds: body.DurationSeconds,
		Comment:         body.Comment,
	})
	if err != nil {
		upstream(err).write(w)
		return
	}

	insertErr := s.store.InsertPushedWorklog(store.PushedWorklog{
		UserID:         user.ID,
		IdempotencyKey: body.IdempotencyKey,
		IssueKey:       body.IssueKey,
		JiraWorklogID:  worklogID,
	})
	if insertErr == store.ErrDuplicate {
		// Two retries of the same session raced and both got past the SELECT. The
		// unique constraint caught the second. Jira now holds a duplicate work log,
		// which this request cannot undo — but it CAN avoid compounding it into a
		// third attempt by reporting the id and moving on.
		if existing, ok, err := s.store.PushedWorklogByKey(user.ID, body.IdempotencyKey); err == nil && ok {
			writeJSON(w, http.StatusCreated, worklogResponse{
				JiraWorklogID: existing.JiraWorklogID, IssueKey: existing.IssueKey, Duplicate: true,
			})
			return
		}
	} else if insertErr != nil {
		writeError(w, http.StatusInternalServerError, "The work log reached Jira but could not be recorded.")
		return
	}

	writeJSON(w, http.StatusCreated, worklogResponse{
		JiraWorklogID: worklogID, IssueKey: body.IssueKey, Duplicate: false,
	})
}

// ---------------------------------------------------------------------------
// Jira binding
// ---------------------------------------------------------------------------

// jiraFor returns a Jira client authenticated as user, refreshing the grant if
// it is stale. The httpError return carries the HTTP status the handler should
// answer with when the grant is missing or dead.
func (s *Server) jiraFor(ctx context.Context, user store.User) (*jira.Client, *httpError) {
	token, ok, err := s.store.GetJiraToken(user.ID)
	if err != nil {
		return nil, &httpError{http.StatusInternalServerError, "Could not read the Jira connection."}
	}
	if !ok {
		return nil, &httpError{http.StatusConflict,
			"This account has not connected Jira yet. Run 'Connect to Jira' in the desktop client's Settings."}
	}

	if token.ExpiresAt.Add(-jira.RefreshSkew).Before(time.Now().UTC()) ||
		token.ExpiresAt.Add(-jira.RefreshSkew).Equal(time.Now().UTC()) {
		refreshed, herr := s.refresh(ctx, token)
		if herr != nil {
			return nil, herr
		}
		token = refreshed
	}

	access, err := s.cipher.Decrypt(token.AccessTokenEnc)
	if err != nil {
		return nil, &httpError{http.StatusInternalServerError, err.Error()}
	}
	return jira.NewClient(s.http, access, token.CloudID, token.SiteURL), nil
}

// refresh exchanges the rotating refresh token and persists the new grant in the
// same step that uses it.
func (s *Server) refresh(ctx context.Context, token store.JiraToken) (store.JiraToken, *httpError) {
	current, err := s.cipher.Decrypt(token.RefreshTokenEnc)
	if err != nil {
		return store.JiraToken{}, &httpError{http.StatusInternalServerError, err.Error()}
	}

	fresh, err := jira.Refresh(ctx, s.http, s.cfg.AtlassianClientID, s.cfg.AtlassianClientSecret, current)
	if err != nil {
		// invalid_grant here means the user revoked us, an admin removed their
		// access, or the grant aged out. None are retryable and all are fixed the
		// same way, so say so instead of surfacing an Atlassian code.
		var authErr *jira.AuthError
		if asAuthError(err, &authErr) {
			return store.JiraToken{}, &httpError{http.StatusConflict,
				"The Jira connection for this account is no longer valid. Run 'Connect to Jira' " +
					"in the desktop client's Settings to reconnect."}
		}
		return store.JiraToken{}, &httpError{http.StatusBadGateway, err.Error()}
	}

	if err := s.storeTokens(token.UserID, fresh, token.CloudID, token.SiteURL); err != nil {
		return store.JiraToken{}, &httpError{http.StatusInternalServerError, "Could not persist the refreshed Jira token."}
	}

	access, err := s.cipher.Encrypt(fresh.AccessToken)
	if err != nil {
		return store.JiraToken{}, &httpError{http.StatusInternalServerError, err.Error()}
	}
	refresh, err := s.cipher.Encrypt(fresh.RefreshToken)
	if err != nil {
		return store.JiraToken{}, &httpError{http.StatusInternalServerError, err.Error()}
	}
	return store.JiraToken{
		UserID:          token.UserID,
		AccessTokenEnc:  access,
		RefreshTokenEnc: refresh,
		ExpiresAt:       fresh.ExpiresAt,
		CloudID:         token.CloudID,
		SiteURL:         token.SiteURL,
	}, nil
}
