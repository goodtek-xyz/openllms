package httpserver

import (
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
)

// Startup lets the gateway listen before start-up (migrations, database connection, secrets) has finished. Until
// Started is called it answers /health and /livez with 200 (liveness), /readyz with 503 and every other request with
// 503; afterwards it hands every request to the handler given to Started, on the same listener.
type Startup struct {
	starting http.Handler
	next     atomic.Pointer[http.Handler]
}

// NewStartup returns a Startup in the starting state.
func NewStartup() *Startup {
	r := chi.NewRouter()
	r.Use(headAsGet)
	r.Get("/health", handleHealth)
	r.Get("/livez", handleHealth)
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "starting"})
	})
	notStarted := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "starting"})
	}
	r.NotFound(notStarted)
	r.MethodNotAllowed(notStarted)
	return &Startup{starting: r}
}

// Started marks start-up as finished: from now on every request goes to h.
func (s *Startup) Started(h http.Handler) {
	s.next.Store(&h)
}

func (s *Startup) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h := s.next.Load(); h != nil {
		(*h).ServeHTTP(w, r)
		return
	}
	s.starting.ServeHTTP(w, r)
}
