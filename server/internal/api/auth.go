package api

import (
	"net/http"
	"strings"

	"task-timer-server/internal/crypto"
	"task-timer-server/internal/store"
)

// authedHandler is a handler that has already resolved the caller's identity.
type authedHandler func(w http.ResponseWriter, r *http.Request, user store.User)

// authed wraps a handler so it runs only for a caller holding a valid bearer
// key. Everything behind it is closed without one.
func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.currentUser(w, r)
		if !ok {
			return
		}
		h(w, r, user)
	}
}

func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		unauthorized(w, "Missing bearer token. Run the desktop client's 'Connect to Jira' once.")
		return store.User{}, false
	}

	key, ok, err := s.store.APIKeyByHash(crypto.HashAPIKey(token))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not check the key.")
		return store.User{}, false
	}
	if !ok || key.RevokedAt != nil {
		unauthorized(w, "That API key is not valid.")
		return store.User{}, false
	}

	// Best effort; a failure to record last-used must not fail the request.
	_ = s.store.TouchAPIKey(key.ID)

	user, ok, err := s.store.UserByID(key.UserID)
	if err != nil || !ok {
		unauthorized(w, "That API key is not valid.")
		return store.User{}, false
	}
	return user, true
}

func unauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, detail)
}

// bearerToken pulls the credential out of an "Authorization: Bearer <token>"
// header, tolerating the case of the scheme.
func bearerToken(header string) string {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(header[len(scheme):])
}
