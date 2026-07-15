// Package jira speaks Atlassian's OAuth 2.0 (3LO) and the Jira Cloud REST API v3.
//
// Three things about Atlassian's implementation drive the shape of this file:
//
//   - The refresh token ROTATES. Every refresh returns a new one and kills the
//     old one immediately. Persisting the new value is not an optimisation;
//     skipping it locks the user out for good.
//   - offline_access is what makes a refresh token appear at all. Without that
//     scope the grant dies in an hour and the user is asked to consent again.
//   - The Jira REST API is NOT at the site's own hostname under 3LO. It is at
//     api.atlassian.com/ex/jira/<cloud_id>, and the cloud_id has to be discovered
//     from /oauth/token/accessible-resources after the exchange.
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

// These are package variables rather than constants so a test can redirect them
// at an httptest server, exactly as the Python suite patched the module's
// endpoint constants.
var (
	AuthorizeURL = "https://auth.atlassian.com/authorize"
	TokenURL     = "https://auth.atlassian.com/oauth/token"
	ResourcesURL = "https://api.atlassian.com/oauth/token/accessible-resources"
	MeURL        = "https://api.atlassian.com/me"
)

// Scopes: read/write:jira-work covers issue search, work logs and transitions.
// offline_access is what buys us a refresh token.
var Scopes = []string{"read:me", "read:jira-work", "write:jira-work", "offline_access"}

// RefreshSkew refreshes a little early. An access token that expires between our
// check and Jira's receipt of the request is a 401 the user never needed to see.
const RefreshSkew = 90 * time.Second

const oauthTimeout = 30 * time.Second

// TokenSet is one grant's access + refresh tokens and the access token's expiry.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Identity is who Atlassian says the connected user is.
type Identity struct {
	AccountID   string
	Email       string
	DisplayName string
}

// Site is the Jira site every subsequent call is addressed to.
type Site struct {
	CloudID string
	URL     string
}

// AuthError means the OAuth grant is unusable and the user has to reconnect.
type AuthError struct{ msg string }

func (e *AuthError) Error() string { return e.msg }

// AuthorizeURLFor is the URL the user's browser is sent to in order to consent.
func AuthorizeURLFor(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("audience", "api.atlassian.com")
	q.Set("client_id", clientID)
	q.Set("scope", strings.Join(Scopes, " "))
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("response_type", "code")
	// Atlassian only issues a refresh token when it is asked to consent afresh;
	// without this an already-consented user silently comes back with no refresh
	// token and the grant expires in an hour.
	q.Set("prompt", "consent")
	return AuthorizeURL + "?" + q.Encode()
}

type tokenPayload struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func toTokenSet(p tokenPayload, fallbackRefresh string) TokenSet {
	expiresIn := p.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 3600
	}
	refresh := p.RefreshToken
	if refresh == "" {
		// The spec permits omitting an unchanged refresh token; dropping it here
		// would be indistinguishable from a revoked grant next time we refreshed.
		refresh = fallbackRefresh
	}
	return TokenSet{
		AccessToken:  p.AccessToken,
		RefreshToken: refresh,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(expiresIn) * time.Second),
	}
}

// ExchangeCode trades an authorization code for a token set.
func ExchangeCode(ctx context.Context, http *http.Client, clientID, clientSecret, code, redirectURI string) (TokenSet, error) {
	var p tokenPayload
	err := postJSON(ctx, http, TokenURL, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
	}, &p, "exchanging the authorization code")
	if err != nil {
		return TokenSet{}, err
	}
	return toTokenSet(p, ""), nil
}

// Refresh exchanges a (rotating) refresh token for a fresh token set.
func Refresh(ctx context.Context, http *http.Client, clientID, clientSecret, refreshToken string) (TokenSet, error) {
	var p tokenPayload
	err := postJSON(ctx, http, TokenURL, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"client_secret": clientSecret,
		"refresh_token": refreshToken,
	}, &p, "refreshing the access token")
	if err != nil {
		return TokenSet{}, err
	}
	return toTokenSet(p, refreshToken), nil
}

// WhoAmI reads the Atlassian profile for an access token.
func WhoAmI(ctx context.Context, httpc *http.Client, accessToken string) (Identity, error) {
	var body struct {
		AccountID string `json:"account_id"`
		Email     string `json:"email"`
		Name      string `json:"name"`
	}
	if err := getJSON(ctx, httpc, MeURL, accessToken, &body, "reading the Atlassian profile"); err != nil {
		return Identity{}, err
	}
	return Identity{AccountID: body.AccountID, Email: body.Email, DisplayName: body.Name}, nil
}

// FirstJiraSite resolves the cloud id every subsequent Jira call is addressed to.
//
// A user may have access to several Atlassian sites. We take the first, which is
// right for the single-tenant deployment this server is built for.
func FirstJiraSite(ctx context.Context, httpc *http.Client, accessToken string) (Site, error) {
	var sites []struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := getJSON(ctx, httpc, ResourcesURL, accessToken, &sites, "listing accessible Atlassian sites"); err != nil {
		return Site{}, err
	}
	if len(sites) == 0 {
		return Site{}, &AuthError{msg: "That Atlassian account has no Jira site available to this app. " +
			"Ask an administrator to grant it access, then connect again."}
	}
	return Site{CloudID: sites[0].ID, URL: sites[0].URL}, nil
}

// ---------------------------------------------------------------------------

func postJSON(ctx context.Context, httpc *http.Client, endpoint string, payload any, out any, what string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return doOAuth(httpc, req, out, what)
}

func getJSON(ctx context.Context, httpc *http.Client, endpoint, accessToken string, out any, what string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	return doOAuth(httpc, req, out, what)
}

func doOAuth(httpc *http.Client, req *http.Request, out any, what string) error {
	ctx, cancel := context.WithTimeout(req.Context(), oauthTimeout)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := httpc.Do(req)
	if err != nil {
		return &AuthError{msg: fmt.Sprintf("%s failed: %v", what, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return oauthError(resp, what)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// oauthError passes Atlassian's own small, specific error through. Its
// "invalid_grant" is the whole diagnosis and beats "unexpected status 400".
func oauthError(resp *http.Response, what string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	detail := ""
	var body struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(raw, &body) == nil {
		if body.ErrorDescription != "" {
			detail = body.ErrorDescription
		} else {
			detail = body.Error
		}
	}
	if detail == "" {
		detail = strings.TrimSpace(string(raw))
		if len(detail) > 200 {
			detail = detail[:200]
		}
	}
	return &AuthError{msg: strings.TrimSpace(
		fmt.Sprintf("%s failed: Atlassian returned %d: %s", what, resp.StatusCode, detail))}
}
