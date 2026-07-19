package handlers

import (
	"net/http"
	"net/url"
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

func (s *Server) parseSearchQuery(r *http.Request, uLimit int) (qText string, kind search.Kind, field string, results []search.Result) {
	u := s.current(r)
	qText = strings.TrimSpace(r.URL.Query().Get("q"))
	kind = search.ParseKind(r.URL.Query().Get("kind"))
	field = search.ParseField(r.URL.Query().Get("field"))
	if qText == "" || s.Search == nil {
		return qText, kind, field, nil
	}
	results, _ = s.Search.Search(search.Query{
		Text:        qText,
		Kind:        kind,
		Field:       field,
		WorkspaceID: u.WorkspaceID,
		OwnerID:     u.ID,
		Admin:       u.IsAdmin(),
		Limit:       uLimit,
	})
	return qText, kind, field, results
}

func (s *Server) searchPageData(r *http.Request, limit int) map[string]any {
	u := s.current(r)
	q, kind, field, results := s.parseSearchQuery(r, limit)
	backend := "none"
	if s.Search != nil {
		backend = s.Search.Backend()
	}
	return map[string]any{
		"Nav":        "search",
		"User":       u,
		"Q":          q,
		"Kind":       string(kind),
		"Field":      field,
		"Results":    results,
		"Backend":    backend,
		"Categories": search.Categories(),
		"Fields":     search.Fields(),
		"SearchURL": func(k, f string) string {
			v := url.Values{}
			if q != "" {
				v.Set("q", q)
			}
			if k != "" {
				v.Set("kind", k)
			}
			if f != "" {
				v.Set("field", f)
			}
			qs := v.Encode()
			if qs == "" {
				return "/search"
			}
			return "/search?" + qs
		},
	}
}

func (s *Server) searchGet(w http.ResponseWriter, r *http.Request) {
	s.render(w, "search.html", s.searchPageData(r, 40))
}

func (s *Server) searchResults(w http.ResponseWriter, r *http.Request) {
	s.render(w, "search_results.html", s.searchPageData(r, 40))
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
