// Package server receives Alertmanager webhooks and runs investigations in
// the background.
//
// Alertmanager re-sends a firing group every group_interval and
// repeat_interval, so the server investigates a group once: deliveries for a
// group that is queued, running, or finished less than Cooldown ago are
// acknowledged and dropped.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hindcast/hindcast/internal/alert"
)

// InvestigateFunc runs one investigation to completion.
type InvestigateFunc func(ctx context.Context, p alert.Payload) error

// Config configures a Server. Zero values take the defaults noted.
type Config struct {
	Investigate InvestigateFunc
	Token       string        // required bearer token; empty disables auth
	Workers     int           // concurrent investigations, default 2
	Queue       int           // waiting investigations, default 32
	Cooldown    time.Duration // default 1h
	Log         *slog.Logger  // default slog.Default()
}

// Server queues webhook deliveries for a fixed pool of workers.
type Server struct {
	cfg  Config
	jobs chan job
	now  func() time.Time

	mu    sync.Mutex
	state map[string]groupState
}

type job struct {
	key     string
	payload alert.Payload
}

type groupState struct {
	active   bool
	finished time.Time
}

const maxBody = 1 << 20

// New returns a Server. Call Run to start the workers.
func New(cfg Config) *Server {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.Queue <= 0 {
		cfg.Queue = 32
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = time.Hour
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Server{cfg: cfg, jobs: make(chan job, cfg.Queue), now: time.Now, state: make(map[string]groupState)}
}

// Run processes queued investigations until ctx is cancelled, then waits
// for running ones to return. Investigations receive ctx, so cancelling it
// also cancels them.
func (s *Server) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range s.cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-s.jobs:
					s.run(ctx, j)
				}
			}
		}()
	}
	wg.Wait()
}

func (s *Server) run(ctx context.Context, j job) {
	log := s.cfg.Log.With("group", j.key, "alert", j.payload.Primary().Name())
	log.Info("investigation started")
	start := s.now()
	if err := s.cfg.Investigate(ctx, j.payload); err != nil {
		log.Error("investigation failed", "err", err, "duration", s.now().Sub(start))
	} else {
		log.Info("investigation finished", "duration", s.now().Sub(start))
	}
	s.mu.Lock()
	s.state[j.key] = groupState{finished: s.now()}
	s.mu.Unlock()
}

// Handler serves POST /alerts and GET /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /alerts", s.handleAlerts)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	})
	return mux
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		reply(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			reply(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		reply(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	p, err := alert.Parse(data)
	if err != nil {
		reply(w, http.StatusBadRequest, err.Error())
		return
	}
	if !p.Firing() {
		reply(w, http.StatusOK, "ignored: nothing firing")
		return
	}

	key := groupKey(p)
	if !s.claim(key) {
		reply(w, http.StatusOK, "ignored: group already investigated")
		return
	}
	select {
	case s.jobs <- job{key: key, payload: p}:
		reply(w, http.StatusAccepted, "queued")
	default:
		s.release(key)
		reply(w, http.StatusServiceUnavailable, "queue full")
	}
}

// claim marks a group active unless it is active or cooling down.
func (s *Server) claim(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state[key]
	if ok && (st.active || s.now().Sub(st.finished) < s.cfg.Cooldown) {
		return false
	}
	s.state[key] = groupState{active: true}
	return true
}

func (s *Server) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state, key)
}

func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		return true
	}
	want := "Bearer " + s.cfg.Token
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) == 1
}

func groupKey(p alert.Payload) string {
	if p.GroupKey != "" {
		return p.GroupKey
	}
	a := p.Primary()
	if a.Fingerprint != "" {
		return a.Fingerprint
	}
	return a.Name()
}

func reply(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"status": msg})
}
