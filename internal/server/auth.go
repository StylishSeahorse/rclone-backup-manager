package server

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "wb_session"
	sessionTTL    = 12 * time.Hour
	jwtIssuer     = "wasabi-backup-dashboard"
)

type ctxKey int

const (
	ctxAgent ctxKey = iota
	ctxUser
)

type agentIdentity struct{ ID, Name string }

// ---- Admin (browser) auth: JWT in an HttpOnly cookie -------------------------

func (s *Server) issueJWT(userID, username string) (string, error) {
	claims := jwt.RegisteredClaims{
		Issuer:    jwtIssuer,
		Subject:   userID,
		Audience:  jwt.ClaimStrings{username},
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(sessionTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.cfg.jwtKey)
}

func (s *Server) parseJWT(tok string) (*jwt.RegisteredClaims, error) {
	claims := &jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(tok, claims, func(*jwt.Token) (any, error) { return s.cfg.jwtKey, nil },
		jwt.WithValidMethods([]string{"HS256"}), // never accept "none" or RSA/HMAC confusion
		jwt.WithIssuer(jwtIssuer),
		jwt.WithExpirationRequired(),
	)
	return claims, err
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var tok string
		if c, err := r.Cookie(sessionCookie); err == nil {
			tok = c.Value
		} else if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimPrefix(h, "Bearer ")
		}
		claims, err := s.parseJWT(tok)
		if tok == "" || err != nil {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		// CSRF: SameSite=Strict already blocks cross-site cookies; additionally
		// require a custom header (not sendable cross-origin without CORS) on writes.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "dashboard" {
			writeErr(w, http.StatusForbidden, "missing X-Requested-With header")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUser, claims.Subject)))
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	var in struct{ Username, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	var id, hash string
	err := s.db.QueryRow(`SELECT id, password_hash FROM users WHERE username = ?`, in.Username).Scan(&id, &hash)
	if err == sql.ErrNoRows {
		hash = dummyHash // equalise timing so usernames can't be enumerated
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil || id == "" {
		s.limiter.fail(ip)
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	tok, err := s.issueJWT(id, in.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"username": in.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// A valid bcrypt hash of a random string, compared against when the user doesn't exist.
var dummyHash = func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.DefaultCost)
	return string(h)
}()

// ---- Agent auth: persistent API key as bearer token --------------------------

func (s *Server) requireAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer wbk_") {
			writeErr(w, http.StatusUnauthorized, "agent API key required")
			return
		}
		key := strings.TrimPrefix(h, "Bearer ")
		var a agentIdentity
		var stored string
		err := s.db.QueryRow(`SELECT id, name, api_key_hash FROM agents WHERE api_key_hash = ?`, hashKey(key)).Scan(&a.ID, &a.Name, &stored)
		if err != nil || !constEq(stored, hashKey(key)) {
			writeErr(w, http.StatusUnauthorized, "invalid agent API key")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxAgent, a)))
	}
}

func agentFrom(r *http.Request) agentIdentity { return r.Context().Value(ctxAgent).(agentIdentity) }

// ---- Login throttling ---------------------------------------------------------

type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

const (
	maxFailures   = 10
	failureWindow = 5 * time.Minute
)

func newLoginLimiter() *loginLimiter { return &loginLimiter{failures: map[string][]time.Time{}} }

func (l *loginLimiter) prune(ip string) []time.Time {
	cut := time.Now().Add(-failureWindow)
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = kept
	}
	return kept
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(ip)) < maxFailures
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.prune(ip), time.Now())
}

// clientIP uses the socket address. Behind a reverse proxy every client would
// share the proxy's IP, which fails safe (stricter throttling), never open.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
