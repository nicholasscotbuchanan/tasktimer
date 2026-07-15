package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"time"

	"task-timer-server/internal/jira"
)

// problem is the error body every 4xx and 5xx carries. The desktop client reads
// only `detail` (see the gateway provider's apiError), so that is all this is.
type problem struct {
	Detail string `json:"detail"`
}

// httpError is a status + detail a handler answers with. It exists so the Jira
// binding can report "grant is gone, 409" back up to the handler without the
// handler re-deriving the status.
type httpError struct {
	status int
	detail string
}

func (e *httpError) write(w http.ResponseWriter) { writeError(w, e.status, e.detail) }

// upstream maps a Jira failure onto a status the client can act on.
//
// A 401/403 from Jira is not the client's fault and must not be reported as one:
// answering 401 would make the desktop app throw away a perfectly good API key
// and demand the user log in to the *backend* again, when the real problem is
// upstream. 502 says plainly that the fault is between us and Jira.
func upstream(err error) *httpError {
	var je *jira.Error
	if errors.As(err, &je) {
		switch je.StatusCode {
		case http.StatusNotFound:
			return &httpError{http.StatusNotFound, je.Error()}
		case http.StatusUnauthorized, http.StatusForbidden:
			return &httpError{http.StatusBadGateway,
				"Jira refused the request for this account: " + je.Error()}
		}
	}
	return &httpError{http.StatusBadGateway, err.Error()}
}

func asAuthError(err error, target **jira.AuthError) bool {
	return errors.As(err, target)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, problem{Detail: detail})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// isLoopback refuses to redirect anywhere but the machine the user is sitting
// at. Without it, /auth/login is an open redirect wearing a Jira costume: hand
// it ?redirect_uri=https://evil.example and it hands a redeemable code to
// whoever asked. The client is a desktop app; loopback only.
func isLoopback(redirectURI string) bool {
	parsed, err := url.Parse(redirectURI)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

func expiredSince(createdAt time.Time, ttl time.Duration) bool {
	if createdAt.IsZero() {
		return false
	}
	return time.Now().UTC().Sub(createdAt) > ttl
}

func expiredAt(when time.Time) bool {
	return time.Now().UTC().After(when)
}

// writePage is the only HTML this server serves: what the user sees after
// consenting. They are looking at a browser tab the desktop app opened; ending
// the flow on a raw JSON body, or on nothing, leaves them unsure it worked.
func writePage(w http.ResponseWriter, title, message string, ok bool) {
	colour := "#cf222e"
	status := http.StatusBadRequest
	if ok {
		colour = "#1f883d"
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html>
<meta charset="utf-8">
<title>Task Timer</title>
<style>
  body { font: 16px/1.5 system-ui, sans-serif; margin: 0; display: grid;
         place-items: center; min-height: 100vh; background: #f6f8fa; color: #1f2328; }
  .card { background: #fff; padding: 2.5rem 3rem; border-radius: 12px; max-width: 30rem;
          box-shadow: 0 1px 3px rgba(0,0,0,.12); text-align: center; }
  h1 { margin: 0 0 .5rem; font-size: 1.25rem; color: `+colour+`; }
  p { margin: 0; color: #656d76; }
  @media (prefers-color-scheme: dark) {
    body { background: #0d1117; color: #e6edf3; }
    .card { background: #161b22; box-shadow: none; }
    p { color: #8b949e; }
  }
</style>
<div class="card"><h1>`+html.EscapeString(title)+`</h1><p>`+html.EscapeString(message)+`</p></div>`)
}
