package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/buildinfo"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/join"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/ui"
)

// ErrHandlersUnjoined reports that Shutdown could not join every active
// handler before its context ended. A handler that ignores cancellation
// keeps running.
var ErrHandlersUnjoined = errors.New("api: active handlers did not finish")

// Server is one running API instance. New builds it without binding;
// Start binds loopback and writes the runtime descriptor.
type Server struct {
	services Services
	opts     Options
	log      *slog.Logger
	clock    clock.Clock

	token     string
	launch    string
	startedAt time.Time

	handler http.Handler
	static  http.Handler
	mux     *http.ServeMux
	hub     *Hub

	// handlers is the one bounded join of the requests this server admitted.
	// Shutdown refuses new ones first, then joins the running ones bounded by
	// its context (join.Group).
	handlers join.Group

	mu      sync.RWMutex // guards port and baseURL
	port    int
	baseURL string

	startMu   sync.Mutex
	started   bool
	srv       *http.Server
	ln        net.Listener
	serveDone chan struct{}

	shutdownOnce sync.Once
	shutdownErr  error
	descriptor   string
	replace      fsx.Replace
}

type descriptor struct {
	Version   int       `json:"version"`
	Launch    string    `json:"launch"`
	PID       int       `json:"pid"`
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	StartedAt time.Time `json:"started_at"`
	Build     string    `json:"build"`
}

// New validates the services and options, generates the per-launch token
// and launch id, and builds the hub and the mux. It does not bind.
func New(services Services, options Options) (*Server, error) {
	if err := services.validate(); err != nil {
		return nil, err
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	token := options.Token
	if token == "" {
		var err error
		if token, err = newToken(); err != nil {
			return nil, err
		}
	}
	launch, err := newLaunchID()
	if err != nil {
		return nil, err
	}
	registerToken(token)
	s := &Server{
		services:   services,
		opts:       options,
		log:        logx.For("api"),
		clock:      clock.Or(options.Clock),
		token:      token,
		launch:     launch,
		descriptor: options.DescriptorPath,
		replace:    options.Replace,
	}
	s.startedAt = s.clock.Now().UTC()
	hub, err := newHub(services.Bus, options)
	if err != nil {
		return nil, fmt.Errorf("api: subscribe to events: %w", err)
	}
	s.hub = hub
	s.static = ui.Handler()
	s.handler = s.buildHandler()
	return s, nil
}

// Handler returns the middleware-wrapped router. Tests may use it with
// httptest without binding.
func (s *Server) Handler() http.Handler { return s.handler }

// URL is the base URL, or "" before Start.
func (s *Server) URL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseURL
}

// BootstrapURL is the only method that returns the token. It is for the
// desktop window's fragment and must never be logged.
func (s *Server) BootstrapURL() string {
	base := s.URL()
	if base == "" {
		return ""
	}
	return base + "#token=" + s.token
}

// Launch is the non-secret id of this process.
func (s *Server) Launch() string { return s.launch }

// Start binds tcp4 127.0.0.1:0, serves in the background and writes the
// runtime descriptor. It may be called once.
func (s *Server) Start() (string, error) {
	s.startMu.Lock()
	if s.started {
		s.startMu.Unlock()
		return "", errors.New("api: server already started")
	}
	s.started = true
	s.startMu.Unlock()

	ln, err := net.Listen("tcp4", bindAddr)
	if err != nil {
		return "", fmt.Errorf("api: listen on loopback: %w", err)
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		_ = ln.Close()
		return "", fmt.Errorf("api: listener is not loopback: %v", ln.Addr())
	}
	port := tcp.Port
	base := "http://127.0.0.1:" + strconv.Itoa(port) + "/"

	s.mu.Lock()
	s.port = port
	s.baseURL = base
	s.ln = ln
	s.mu.Unlock()

	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		// HeaderTimeout only bounds headers. A stalled body would hold the
		// handler goroutine and its connection forever; every routed request
		// already has this deadline, so the read side uses the same owner.
		ReadTimeout:    s.opts.RequestTimeout,
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: maxHeaderBytes,
		ErrorLog:       slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	done := make(chan struct{})
	s.startMu.Lock()
	s.srv = srv
	s.serveDone = done
	s.startMu.Unlock()
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("api server stopped unexpectedly", "err", err)
		}
	}()

	if s.descriptor != "" {
		data, err := marshal(descriptor{Version: descriptorVersion, Launch: s.launch, PID: os.Getpid(), URL: base,
			Token: s.token, StartedAt: s.startedAt, Build: buildinfo.Version})
		if err != nil {
			_ = srv.Close()
			<-done
			return "", fmt.Errorf("api: encode runtime descriptor: %w", err)
		}
		if err := fsx.WriteFileAtomic(s.descriptor, data, fsx.FilePerm, s.replace); err != nil {
			_ = srv.Close()
			<-done
			return "", fmt.Errorf("api: write runtime descriptor: %w", err)
		}
	}
	s.log.Info("api listening", "url", base, "descriptor", s.descriptor)
	return base, nil
}

// Shutdown closes the SSE hub and the HTTP server, waits for the serve
// loop and every active handler, then removes the descriptor when it still
// names this launch. A handler that outlives ctx is reported as
// ErrHandlersUnjoined so the caller can keep the store open.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		// Refuse new handlers before the listener drains, then join the ones
		// already running once it has. Close does both; split so the drain
		// never runs with the gate still open.
		s.handlers.Refuse()
		s.hub.Close()
		s.startMu.Lock()
		started, srv, done := s.started, s.srv, s.serveDone
		s.startMu.Unlock()
		if started && srv != nil {
			if err := srv.Shutdown(ctx); err != nil {
				s.shutdownErr = err
				_ = srv.Close()
			}
			if done != nil {
				<-done
			}
		}
		if err := s.joinHandlers(ctx); err != nil {
			s.shutdownErr = errors.Join(s.shutdownErr, err)
		}
		s.removeDescriptor()
		if s.shutdownErr != nil {
			s.log.Warn("api shutdown did not finish cleanly", "err", s.shutdownErr)
		} else {
			s.log.Info("api stopped")
		}
	})
	return s.shutdownErr
}

// enterHandler admits one request to the router. Once Shutdown has refused the
// group, no new handler is counted and the request is refused.
func (s *Server) enterHandler() bool { return s.handlers.Track() }

// leaveHandler releases one admitted request.
func (s *Server) leaveHandler() { s.handlers.Done() }

// joinHandlers waits for every admitted handler, bounded by ctx.
func (s *Server) joinHandlers(ctx context.Context) error {
	if s.handlers.Close(ctx) {
		return nil
	}
	// A handler may have finished between the bound and this read; only a
	// handler still running is an unjoined one.
	if active := s.handlers.Active(); active > 0 {
		return fmt.Errorf("%w: %d handler(s) still running: %w", ErrHandlersUnjoined, active, ctx.Err())
	}
	return nil
}

func (s *Server) removeDescriptor() {
	if s.descriptor == "" {
		return
	}
	data, ok, err := fsx.ReadOptional(s.descriptor)
	if err != nil || !ok {
		return
	}
	var d descriptor
	if err := jsonx.DecodeStrict(data, &d); err != nil {
		return // never delete a file we cannot identify
	}
	if d.Launch != s.launch {
		return
	}
	if err := os.Remove(s.descriptor); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("cannot remove the runtime descriptor", "path", filepath.Clean(s.descriptor), "err", err)
	}
}

func (s *Server) boundPort() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.port
}
