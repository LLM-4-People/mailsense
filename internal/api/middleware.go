package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// response tracks whether a header was written so panic recovery can
// decide between a JSON 500 and aborting the connection.
type response struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *response) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status, w.wrote = status, true
	w.ResponseWriter.WriteHeader(status)
}

func (w *response) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.NewResponseController find Flush and the deadlines.
func (w *response) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *response) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &response{ResponseWriter: w}
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			s.log.Error("api panic", "panic_type", fmt.Sprintf("%T", rec), "method", r.Method, "route", r.Pattern,
				"stack", string(debug.Stack()))
			if rw.wrote {
				panic(http.ErrAbortHandler)
			}
			s.writeAPIError(rw, r, http.StatusInternalServerError, &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"})
		}()

		if !s.hostAllowed(r) {
			s.forbid(rw, r, "forbidden_host", "the Host header is not this loopback address")
			return
		}
		// ServeMux matches on the escaped path (r.URL.EscapedPath()): a
		// request for /api/%2e%2e/status reaches the /api/ subtree even
		// though the decoded path cleans to /status. The gate must decide
		// on the same form the mux matches, so either form naming an API
		// path is gated.
		api := isAPIPath(r.URL.Path) || isAPIPath(r.URL.EscapedPath())
		if api && !s.originAllowed(r) {
			s.forbid(rw, r, "forbidden_origin", "the request origin is not this app")
			return
		}
		s.securityHeaders(rw)
		if api {
			if !s.authenticate(r) {
				rw.Header().Set("WWW-Authenticate", "Bearer")
				s.log.Warn("api unauthorized", "method", r.Method, "remote", r.RemoteAddr)
				s.writeAPIError(rw, r, http.StatusUnauthorized, &APIError{Status: http.StatusUnauthorized, Code: "unauthorized",
					Message: "missing or invalid bearer token"})
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(rw, r.Body, s.opts.MaxBytes)
			}
		}
		if !s.enterHandler() {
			if api {
				s.writeAPIError(rw, r, http.StatusServiceUnavailable, shuttingDown())
			} else {
				http.Error(rw, "the app is shutting down", http.StatusServiceUnavailable)
			}
			return
		}
		defer s.leaveHandler()
		next.ServeHTTP(rw, r)
	})
}

// isAPIPath reports whether the request names an API path after the mux's
// own canonicalisation. ServeMux would collapse "//api/status" or
// "/x/../api/status" into "/api/status" with a redirect, so auth must decide
// on the same path, not only on a literal "/api/" prefix.
func isAPIPath(p string) bool {
	cleaned := path.Clean("/" + p)
	return cleaned == "/api" || strings.HasPrefix(cleaned, "/api/")
}

func (s *Server) securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
}

func (s *Server) staticHeaders(w http.ResponseWriter) {
	s.securityHeaders(w)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' blob:; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; frame-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
}

// loopbackAuthority is the one rule of both the Host and the Origin check:
// the authority must be the loopback address with an explicit, valid port,
// and that port must be the one this server bound (any port before the
// listener is announced). Two copies of this rule would let one drift, and
// the Host check is what stops DNS rebinding while the Origin check is what
// stops a cross-site request; a gap in either is an entry point.
func (s *Server) loopbackAuthority(authority string) bool {
	host, portText, err := net.SplitHostPort(authority)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	want := s.boundPort()
	return want == 0 || port == want
}

func (s *Server) hostAllowed(r *http.Request) bool {
	return s.loopbackAuthority(r.Host)
}

func (s *Server) originAllowed(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && strings.EqualFold(strings.TrimSpace(site), "cross-site") {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return s.loopbackAuthority(u.Host)
}

func (s *Server) forbid(w http.ResponseWriter, r *http.Request, code, message string) {
	s.securityHeaders(w)
	s.writeAPIError(w, r, http.StatusForbidden, &APIError{Status: http.StatusForbidden, Code: code, Message: message})
}

func (s *Server) gated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.services.Learning.Allow(r.Context()); err != nil {
			s.writeError(w, r, err)
			return
		}
		next(w, r)
	}
}

// route gives one handler its request deadline and its per-response write
// deadline. Stalled local readers cannot pin a goroutine forever.
func (s *Server) route(long bool, next http.HandlerFunc) http.Handler {
	timeout := s.opts.RequestTimeout
	if long {
		timeout = s.opts.LongTimeout
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout))
		next(w, r.WithContext(ctx))
	})
}

func (s *Server) methodNotAllowed(allow string) http.Handler {
	return s.route(false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		s.writeAPIError(w, r, http.StatusMethodNotAllowed, &APIError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed",
			Message: "method not allowed"})
	})
}

func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	s.writeError(w, r, notFound())
}
