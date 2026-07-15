package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"task-timer-server/internal/config"
	"task-timer-server/internal/crypto"
	"task-timer-server/internal/jira"
	"task-timer-server/internal/store"
)

const redirect = "http://127.0.0.1:53682/callback"

// harness is a running gateway plus the fake Atlassian/Jira upstream it talks
// to. The upstream lets each test dictate what the token endpoint and the Jira
// worklog endpoint return, and records what reached Jira.
type harness struct {
	api      *httptest.Server
	store    *store.Store
	cipher   *crypto.Cipher
	upstream *fakeUpstream
	client   *http.Client
}

type fakeUpstream struct {
	srv *httptest.Server

	mu sync.Mutex
	// tokenBodies is a queue of JSON bodies the /token endpoint returns; it
	// stays on the last once exhausted. Empty means the at-1/rt-1 default.
	tokenBodies []string
	// worklog behaviour.
	worklogStatus  int
	worklogBody    string
	worklogCalls   int
	lastWorklogTok string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	up := &fakeUpstream{worklogStatus: 201, worklogBody: `{"id":"10001"}`}
	up.srv = httptest.NewServer(http.HandlerFunc(up.handle))

	// Point the jira package's endpoints at the fake, and restore on cleanup.
	prevToken, prevMe, prevRes, prevBase := jira.TokenURL, jira.MeURL, jira.ResourcesURL, jira.APIBase
	jira.TokenURL = up.srv.URL + "/token"
	jira.MeURL = up.srv.URL + "/me"
	jira.ResourcesURL = up.srv.URL + "/resources"
	jira.APIBase = up.srv.URL + "/ex/jira"

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := crypto.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open("sqlite://")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Settings{
		PublicURL:             "https://timer.example.com",
		DatabaseURL:           "sqlite://",
		AtlassianClientID:     "test-client-id",
		AtlassianClientSecret: "test-client-secret",
		JiraJQL:               "assignee = currentUser() AND statusCategory != Done",
		JiraDoneTransition:    "Done",
		JiraAllowComplete:     true,
		TokenEncryptionKey:    key,
	}

	apiSrv := httptest.NewServer(New(st, cfg, cipher).Handler())

	t.Cleanup(func() {
		apiSrv.Close()
		st.Close()
		up.srv.Close()
		jira.TokenURL, jira.MeURL, jira.ResourcesURL, jira.APIBase = prevToken, prevMe, prevRes, prevBase
	})

	return &harness{
		api:      apiSrv,
		store:    st,
		cipher:   cipher,
		upstream: up,
		// Do not follow redirects: the tests inspect the 307/303 Location headers.
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

func (u *fakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()

	switch {
	case r.URL.Path == "/token":
		body := `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600}`
		if len(u.tokenBodies) > 0 {
			body = u.tokenBodies[0]
			if len(u.tokenBodies) > 1 {
				u.tokenBodies = u.tokenBodies[1:]
			}
		}
		w.Write([]byte(body))
	case r.URL.Path == "/me":
		w.Write([]byte(`{"account_id":"acct-1","email":"alice@acme.com","name":"Alice"}`))
	case r.URL.Path == "/resources":
		w.Write([]byte(`[{"id":"cloud-123","url":"https://acme.atlassian.net"}]`))
	case strings.HasSuffix(r.URL.Path, "/worklog"):
		u.worklogCalls++
		u.lastWorklogTok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(u.worklogStatus)
		w.Write([]byte(u.worklogBody))
	default:
		http.NotFound(w, r)
	}
}

// ---------------------------------------------------------------------------
// request helpers
// ---------------------------------------------------------------------------

func (h *harness) get(t *testing.T, path string, header http.Header) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.api.URL+path, nil)
	req.Header = header
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (h *harness) postJSON(t *testing.T, path string, body any, header http.Header) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, h.api.URL+path, bytes.NewReader(raw))
	if header == nil {
		header = http.Header{}
	}
	header.Set("Content-Type", "application/json")
	req.Header = header
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func auth(key string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+key)
	return h
}

func decode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var m map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return m
}

// begin kicks off a login and returns the state Atlassian would echo back.
func (h *harness) begin(t *testing.T, verifier string) string {
	t.Helper()
	q := url.Values{}
	q.Set("redirect_uri", redirect)
	q.Set("code_challenge", crypto.PKCEChallenge(verifier))
	q.Set("state", "client-state")
	resp := h.get(t, "/auth/login?"+q.Encode(), http.Header{})
	defer resp.Body.Close()
	if resp.StatusCode != 307 {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, jira.AuthorizeURL) {
		t.Fatalf("login did not redirect to Atlassian: %q", loc)
	}
	locQuery, _ := url.Parse(loc)
	scope := locQuery.Query().Get("scope")
	if !strings.Contains(scope, "offline_access") {
		t.Fatalf("scope missing offline_access: %q", scope)
	}
	return locQuery.Query().Get("state")
}

// register runs the whole consent flow and returns an issued API key.
func (h *harness) register(t *testing.T) string {
	t.Helper()
	verifier := strings.Repeat("v", 64)
	state := h.begin(t, verifier)
	resp := h.get(t, "/auth/callback?"+url.Values{"code": {"c"}, "state": {state}}.Encode(), http.Header{})
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")
	ex := h.postJSON(t, "/api/v1/auth/exchange",
		map[string]string{"code": code, "code_verifier": verifier}, nil)
	return decode(t, ex)["api_key"].(string)
}

// ---------------------------------------------------------------------------
// auth flow
// ---------------------------------------------------------------------------

func TestFirstConsentRegistersAndIssuesKey(t *testing.T) {
	h := newHarness(t)
	verifier := strings.Repeat("v", 64)
	state := h.begin(t, verifier)

	resp := h.get(t, "/auth/callback?"+url.Values{"code": {"jira-code"}, "state": {state}}.Encode(), http.Header{})
	resp.Body.Close()
	if resp.StatusCode != 303 {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	bounced, _ := url.Parse(loc)
	if bounced.Scheme+"://"+bounced.Host+bounced.Path != redirect {
		t.Fatalf("bounced to %q", loc)
	}
	if bounced.Query().Get("state") != "client-state" {
		t.Fatalf("client state not echoed: %q", loc)
	}
	// The key is NOT in that URL.
	if strings.Contains(loc, "api_key") || strings.Contains(loc, "tt_") {
		t.Fatalf("key leaked into redirect: %q", loc)
	}
	oneTimeCode := bounced.Query().Get("code")

	ex := h.postJSON(t, "/api/v1/auth/exchange",
		map[string]string{"code": oneTimeCode, "code_verifier": verifier}, nil)
	if ex.StatusCode != 200 {
		t.Fatalf("exchange status = %d", ex.StatusCode)
	}
	body := decode(t, ex)
	key, _ := body["api_key"].(string)
	if !strings.HasPrefix(key, "tt_") {
		t.Fatalf("api key = %q", key)
	}
	user := body["user"].(map[string]any)
	if user["email"] != "alice@acme.com" || user["jira_connected"] != true {
		t.Fatalf("user = %v", user)
	}

	// And the key works. Nobody provisioned anything.
	me := h.get(t, "/api/v1/me", auth(key))
	if me.StatusCode != 200 {
		t.Fatalf("me status = %d", me.StatusCode)
	}
	if decode(t, me)["display_name"] != "Alice" {
		t.Fatal("wrong display name")
	}
}

func TestWrongPKCEVerifierRefused(t *testing.T) {
	h := newHarness(t)
	verifier := strings.Repeat("v", 64)
	state := h.begin(t, verifier)
	resp := h.get(t, "/auth/callback?"+url.Values{"code": {"c"}, "state": {state}}.Encode(), http.Header{})
	resp.Body.Close()
	code := mustQuery(resp.Header.Get("Location"), "code")

	ex := h.postJSON(t, "/api/v1/auth/exchange",
		map[string]string{"code": code, "code_verifier": strings.Repeat("x", 64)}, nil)
	if ex.StatusCode != 400 {
		t.Fatalf("status = %d", ex.StatusCode)
	}
	if !strings.Contains(decode(t, ex)["detail"].(string), "PKCE") {
		t.Fatal("detail should mention PKCE")
	}
}

func TestCodeRedeemableOnlyOnce(t *testing.T) {
	h := newHarness(t)
	verifier := strings.Repeat("v", 64)
	state := h.begin(t, verifier)
	resp := h.get(t, "/auth/callback?"+url.Values{"code": {"c"}, "state": {state}}.Encode(), http.Header{})
	resp.Body.Close()
	code := mustQuery(resp.Header.Get("Location"), "code")

	first := h.postJSON(t, "/api/v1/auth/exchange", map[string]string{"code": code, "code_verifier": verifier}, nil)
	first.Body.Close()
	if first.StatusCode != 200 {
		t.Fatalf("first status = %d", first.StatusCode)
	}
	second := h.postJSON(t, "/api/v1/auth/exchange", map[string]string{"code": code, "code_verifier": verifier}, nil)
	second.Body.Close()
	if second.StatusCode != 400 {
		t.Fatalf("second status = %d", second.StatusCode)
	}
}

func TestNonLoopbackRedirectRefused(t *testing.T) {
	h := newHarness(t)
	q := url.Values{}
	q.Set("redirect_uri", "https://evil.example/steal")
	q.Set("code_challenge", crypto.PKCEChallenge(strings.Repeat("v", 64)))
	resp := h.get(t, "/auth/login?"+q.Encode(), http.Header{})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(decode(t, resp)["detail"].(string), "loopback") {
		t.Fatal("detail should mention loopback")
	}
}

func TestSecondConsentReusesAccount(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 2; i++ {
		h.register(t)
	}
	n, err := h.store.CountUsers()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("user count = %d, want 1", n)
	}
}

func TestEndpointsClosedWithoutKey(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		method, path string
	}{{"GET", "/api/v1/me"}, {"GET", "/api/v1/tasks"}} {
		resp := h.get(t, tc.path, http.Header{})
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
	resp := h.postJSON(t, "/api/v1/worklogs", map[string]any{}, nil)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("worklogs = %d, want 401", resp.StatusCode)
	}
}

func TestBogusKeyRefused(t *testing.T) {
	h := newHarness(t)
	resp := h.get(t, "/api/v1/me", auth("tt_nope"))
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// worklogs
// ---------------------------------------------------------------------------

var session = map[string]any{
	"issue_key":        "ENG-412",
	"started":          "2024-03-01T09:15:00+00:00",
	"duration_seconds": 1500,
	"comment":          "traced the drift",
	"idempotency_key":  "7f3c1e0a-2b44-4d9e-9f16-0a1b2c3d4e5f",
}

func TestSessionWrittenAsTheUser(t *testing.T) {
	h := newHarness(t)
	key := h.register(t)

	resp := h.postJSON(t, "/api/v1/worklogs", session, auth(key))
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body := decode(t, resp)
	if body["jira_worklog_id"] != "10001" || body["issue_key"] != "ENG-412" || body["duplicate"] != false {
		t.Fatalf("body = %v", body)
	}
	// Alice's own access token, not a shared bot's.
	if h.upstream.lastWorklogTok != "at-1" {
		t.Fatalf("worklog authed as %q, want at-1", h.upstream.lastWorklogTok)
	}
}

func TestRetryDoesNotLogTwice(t *testing.T) {
	h := newHarness(t)
	key := h.register(t)

	first := decode(t, h.postJSON(t, "/api/v1/worklogs", session, auth(key)))
	second := decode(t, h.postJSON(t, "/api/v1/worklogs", session, auth(key)))

	if first["duplicate"] != false || second["duplicate"] != true {
		t.Fatalf("duplicate flags: %v / %v", first["duplicate"], second["duplicate"])
	}
	if second["jira_worklog_id"] != "10001" {
		t.Fatalf("second id = %v", second["jira_worklog_id"])
	}
	// The important assertion: Jira has no idempotency of its own.
	if h.upstream.worklogCalls != 1 {
		t.Fatalf("worklog calls = %d, want 1", h.upstream.worklogCalls)
	}
}

func TestDifferentSessionGetsThrough(t *testing.T) {
	h := newHarness(t)
	key := h.register(t)

	h.postJSON(t, "/api/v1/worklogs", session, auth(key)).Body.Close()

	other := map[string]any{}
	for k, v := range session {
		other[k] = v
	}
	other["idempotency_key"] = "a-different-session-entirely"
	h.upstream.worklogBody = `{"id":"10002"}`
	resp := decode(t, h.postJSON(t, "/api/v1/worklogs", other, auth(key)))
	if resp["jira_worklog_id"] != "10002" {
		t.Fatalf("id = %v", resp["jira_worklog_id"])
	}
	if h.upstream.worklogCalls != 2 {
		t.Fatalf("calls = %d, want 2", h.upstream.worklogCalls)
	}
}

func TestExpiredGrantIsRefreshedAndRotatedTokenKept(t *testing.T) {
	h := newHarness(t)
	key := h.register(t)

	// Age the access token out by rewriting its expiry to the past, keeping the
	// (still valid) encrypted tokens in place.
	user, _, _ := h.store.UserByAtlassianID("acct-1")
	tok, _, _ := h.store.GetJiraToken(user.ID)
	tok.ExpiresAt = time.Now().UTC().Add(-2 * time.Hour)
	if err := h.store.UpsertJiraToken(tok); err != nil {
		t.Fatal(err)
	}

	// The refresh returns a rotated pair.
	h.upstream.tokenBodies = []string{`{"access_token":"at-2","refresh_token":"rt-2","expires_in":3600}`}

	resp := h.postJSON(t, "/api/v1/worklogs", session, auth(key))
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The refreshed token was used...
	if h.upstream.lastWorklogTok != "at-2" {
		t.Fatalf("worklog authed as %q, want at-2", h.upstream.lastWorklogTok)
	}
	// ...and the ROTATED refresh token was persisted.
	fresh, _, _ := h.store.GetJiraToken(user.ID)
	got, err := h.cipher.Decrypt(fresh.RefreshTokenEnc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "rt-2" {
		t.Fatalf("persisted refresh token = %q, want rt-2", got)
	}
}

func TestJiraRefusalIsNotClientsFault(t *testing.T) {
	h := newHarness(t)
	key := h.register(t)

	h.upstream.worklogStatus = 403
	h.upstream.worklogBody = `{"errorMessages":["no permission on this board"]}`

	resp := h.postJSON(t, "/api/v1/worklogs", session, auth(key))
	// 502, not 401.
	if resp.StatusCode != 502 {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if !strings.Contains(decode(t, resp)["detail"].(string), "no permission") {
		t.Fatal("detail should carry Jira's complaint")
	}
}

func TestTokensEncryptedAtRest(t *testing.T) {
	h := newHarness(t)
	h.register(t)

	user, _, _ := h.store.UserByAtlassianID("acct-1")
	tok, ok, _ := h.store.GetJiraToken(user.ID)
	if !ok {
		t.Fatal("no token stored")
	}
	if strings.Contains(tok.RefreshTokenEnc, "rt-1") || strings.Contains(tok.AccessTokenEnc, "at-1") {
		t.Fatal("token stored in the clear")
	}
}

// ---------------------------------------------------------------------------

func mustQuery(rawurl, key string) string {
	u, _ := url.Parse(rawurl)
	return u.Query().Get(key)
}
