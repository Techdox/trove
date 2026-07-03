package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/techdox/trove/internal/store"
)

// agentHandler is a handler that has been given the authenticated agent.
type agentHandler func(w http.ResponseWriter, r *http.Request, agent store.Agent)

// requireAgent authenticates the bearer token and passes the resolved agent to
// next. Any failure returns 401 with no detail (don't leak whether a token
// exists).
func (s *Server) requireAgent(next agentHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}
		agent, err := s.store.AuthenticateByToken(r.Context(), token)
		if errors.Is(err, store.ErrAgentNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if err != nil {
			s.log.Error("auth lookup", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		next(w, r, agent)
	}
}

// bearerToken extracts a bearer token from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
