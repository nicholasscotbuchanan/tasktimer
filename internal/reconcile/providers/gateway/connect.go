package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"task-timer-app/internal/reconcile"
)

// connectTimeout bounds the whole flow. A user who wandered off mid-consent
// should not leave a listener bound to a loopback port for the rest of the day.
const connectTimeout = 5 * time.Minute

// Identity is who the gateway says the connected user is.
type Identity struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	SiteURL     string `json:"jira_site_url"`
}

// Connect signs this machine in to the gateway and returns a bearer token.
//
// This is OAuth for a native app (RFC 8252): a loopback redirect plus PKCE. We
// open a listener on 127.0.0.1, send the user's browser to the gateway, and the
// gateway — after the user has consented to Atlassian — bounces a one-time code
// back to that listener. The code is then traded for the token over TLS, in a
// request body, proving possession of the PKCE verifier as it goes.
//
// The token deliberately does not travel in the redirect URL. A URL ends up in
// browser history, in the access log of every proxy on the path, and in the
// Referer header of whatever the page loads next.
func Connect(ctx context.Context, baseURL string) (string, Identity, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", Identity{}, errors.New("gateway: no URL to connect to; set the Gateway URL in Settings first")
	}

	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	verifier, err := randomString(64)
	if err != nil {
		return "", Identity{}, err
	}
	state, err := randomString(32)
	if err != nil {
		return "", Identity{}, err
	}

	// Port 0: the OS picks a free one. Hard-coding a port means a second client,
	// or anything else already holding it, breaks sign-in with a bind error.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", Identity{}, fmt.Errorf("gateway: could not open a local callback port: %w", err)
	}
	defer listener.Close()

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)

	codes := make(chan string, 1)
	errs := make(chan error, 1)

	server := &http.Server{
		Handler:           http.HandlerFunc(callbackHandler(state, codes, errs)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	defer func() {
		shutdown, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()

	q := url.Values{}
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", challenge(verifier))
	q.Set("state", state)
	authURL := baseURL + "/auth/login?" + q.Encode()

	if err := openBrowser(authURL); err != nil {
		// Not fatal. A headless box, an SSH session, or a locked-down desktop may
		// have no browser to open — the user can still paste the URL somewhere
		// that does, and the listener is already waiting.
		fmt.Printf("Open this in a browser to connect:\n\n  %s\n\n", authURL)
	}

	select {
	case <-ctx.Done():
		return "", Identity{}, errors.New("gateway: timed out waiting for the browser sign-in to finish")
	case err := <-errs:
		return "", Identity{}, err
	case code := <-codes:
		return exchange(ctx, baseURL, code, verifier)
	}
}

// callbackHandler receives the gateway's redirect and hands the code back.
func callbackHandler(wantState string, codes chan<- string, errs chan<- error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// Constant-time, and checked before the code is touched. Without this, any
		// process on the machine that can guess the port can drive a code of its
		// own choosing into this listener.
		gotState := q.Get("state")
		if subtle.ConstantTimeCompare([]byte(gotState), []byte(wantState)) != 1 {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errs <- errors.New("gateway: the sign-in came back with the wrong state; nothing was connected")
			return
		}

		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code", http.StatusBadRequest)
			errs <- errors.New("gateway: the sign-in came back without a code")
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(donePage))
		codes <- code
	}
}

type exchangeRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

type exchangeResponse struct {
	APIKey string   `json:"api_key"`
	User   Identity `json:"user"`
}

// exchange trades the one-time code for the lasting bearer token.
func exchange(ctx context.Context, baseURL, code, verifier string) (string, Identity, error) {
	body, err := json.Marshal(exchangeRequest{Code: code, CodeVerifier: verifier})
	if err != nil {
		return "", Identity{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/api/v1/auth/exchange", strings.NewReader(string(body)))
	if err != nil {
		return "", Identity{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: defaultTimeout}).Do(req)
	if err != nil {
		return "", Identity{}, fmt.Errorf("gateway: exchanging the sign-in code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", Identity{}, apiError(http.MethodPost, baseURL+"/api/v1/auth/exchange", resp)
	}

	var out exchangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Identity{}, fmt.Errorf("gateway: decoding the sign-in response: %w", err)
	}
	if out.APIKey == "" {
		return "", Identity{}, errors.New("gateway: the sign-in succeeded but returned no token")
	}
	return out.APIKey, out.User, nil
}

// connect is the sync.Connector the desktop app drives through sync.Connect. It
// signs in, then stores the bearer token where the daemon will find it, so the
// app never has to import this package to link a machine to the gateway.
//
// It honours a custom api_token_env the user may have set in the config, falling
// back to the default variable name otherwise.
func connect(ctx context.Context, baseURL string) (reconcile.Identity, error) {
	token, who, err := Connect(ctx, baseURL)
	if err != nil {
		return reconcile.Identity{}, err
	}
	if _, err := SaveToken(configuredConfig(), token); err != nil {
		return reconcile.Identity{}, err
	}
	return reconcile.Identity{
		Email:       who.Email,
		DisplayName: who.DisplayName,
		SiteURL:     who.SiteURL,
	}, nil
}

// hasToken reports whether a bearer token is already available: inline in the
// config, in the process environment, or in the daemon's env file. It lets the
// app skip the sign-in prompt on a machine that is already connected.
func hasToken() bool {
	cfg := configuredConfig()

	if strings.TrimSpace(cfg.APIToken) != "" {
		return true
	}

	name := strings.TrimSpace(cfg.APITokenEnv)
	if name == "" {
		name = defaultTokenEnv
	}

	if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" {
		return true
	}

	names, err := reconcile.EnvNames(reconcile.EnvPath())
	if err != nil {
		return false
	}
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// configuredConfig reads this provider's block from the config so the
// connect flow honours the user's settings — chiefly a custom api_token_env. A
// missing or unparsable config yields the zero Config, whose defaults are sound.
func configuredConfig() Config {
	var cfg Config
	c, err := reconcile.LoadConfig(reconcile.ConfigPath())
	if err != nil {
		return cfg
	}
	for _, pc := range c.Providers {
		if pc.Name == ProviderName && len(pc.Settings) > 0 {
			_ = json.Unmarshal(pc.Settings, &cfg)
			return cfg
		}
	}
	return cfg
}

// SaveToken writes the bearer token into the daemon's env file, under the
// variable name the gateway config points at.
//
// It goes to credentials.env rather than config.yaml for the same reason every other
// secret in this program does: the config file is the thing people paste into
// support tickets.
func SaveToken(cfg Config, token string) (string, error) {
	name := strings.TrimSpace(cfg.APITokenEnv)
	if name == "" {
		name = defaultTokenEnv
	}
	path := reconcile.EnvPath()
	if err := reconcile.SetEnvVar(path, name, token); err != nil {
		return "", err
	}
	return path, nil
}

// ---------------------------------------------------------------------------

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gateway: generating a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// challenge is the PKCE S256 transform of the verifier (RFC 7636).
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// openBrowser is best-effort; the caller falls back to printing the URL.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

const donePage = `<!doctype html>
<meta charset="utf-8">
<title>Task Timer</title>
<style>
  body { font: 16px/1.5 system-ui, sans-serif; margin: 0; display: grid;
         place-items: center; min-height: 100vh; background: #f6f8fa; color: #1f2328; }
  .card { background: #fff; padding: 2.5rem 3rem; border-radius: 12px;
          box-shadow: 0 1px 3px rgba(0,0,0,.12); text-align: center; }
  h1 { margin: 0 0 .5rem; font-size: 1.25rem; color: #1f883d; }
  p { margin: 0; color: #656d76; }
  @media (prefers-color-scheme: dark) {
    body { background: #0d1117; color: #e6edf3; }
    .card { background: #161b22; box-shadow: none; }
    p { color: #8b949e; }
  }
</style>
<div class="card">
  <h1>Task Timer is connected</h1>
  <p>You can close this tab and go back to the app.</p>
</div>`
