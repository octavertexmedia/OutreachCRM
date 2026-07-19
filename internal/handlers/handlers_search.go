package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/search"
)

func (s *Server) registerSearchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /search", s.searchGet)
	mux.HandleFunc("GET /search/results", s.searchResults)
	mux.HandleFunc("POST /search/reindex", s.searchReindex)
}

func (s *Server) indexDocs(docs ...search.Document) {
	if s.Search == nil || len(docs) == 0 {
		return
	}
	s.Search.Upsert(docs)
}

func (s *Server) searchGet(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	backend := "none"
	if s.Search != nil {
		backend = s.Search.Backend()
	}
	var results []search.Result
	if q != "" && s.Search != nil {
		results, _ = s.Search.Search(search.Query{
			Text:        q,
			WorkspaceID: u.WorkspaceID,
			OwnerID:     u.ID,
			Admin:       u.IsAdmin(),
			Limit:       40,
		})
	}
	s.render(w, "search.html", map[string]any{
		"Nav": "search", "User": u, "Q": q, "Results": results, "Backend": backend,
	})
}

func (s *Server) searchResults(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var results []search.Result
	if q != "" && s.Search != nil {
		results, _ = s.Search.Search(search.Query{
			Text:        q,
			WorkspaceID: u.WorkspaceID,
			OwnerID:     u.ID,
			Admin:       u.IsAdmin(),
			Limit:       25,
		})
	}
	s.render(w, "search_results.html", map[string]any{
		"User": u, "Q": q, "Results": results,
	})
}

func (s *Server) searchReindex(w http.ResponseWriter, r *http.Request) {
	u := s.current(r)
	if !u.IsAdmin() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.Search == nil {
		http.Error(w, "search unavailable", http.StatusServiceUnavailable)
		return
	}
	n, err := s.Search.Reindex()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("reindexed " + strconv.Itoa(n) + " docs (" + s.Search.Backend() + ")"))
}
