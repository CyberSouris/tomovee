package webserver

import (
	"encoding/json"
	"net/http"

	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/scan"
	"github.com/cybersouris/tomovee/internal/scanner"
)

type match_request struct {
	Tmdb_id    int    `json:"tmdb_id"`
	Imdb_id    string `json:"imdb_id"`
	Media_type string `json:"media_type"`
}

// handle_manual_match re-matches an unmatched catalog entry against a TMDB or
// IMDb identifier chosen by the user.
func (s *Server) handle_manual_match(w http.ResponseWriter, r *http.Request) {
	if s.matcher == nil {
		write_error(w, http.StatusServiceUnavailable, "matcher is not configured")
		return
	}
	if s.metadata == nil {
		write_error(w, http.StatusServiceUnavailable, "manual matching unavailable: no TMDB API key configured")
		return
	}
	id, ok := path_id(r)
	if !ok {
		write_error(w, http.StatusBadRequest, "invalid catalog id")
		return
	}
	var request match_request
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		write_error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	entry, err := s.store.Get_catalog_entry(r.Context(), id)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil {
		write_error(w, http.StatusNotFound, "catalog entry not found")
		return
	}

	media_type := scanner.Movie
	switch {
	case request.Media_type == "series" || entry.Media_type == "series":
		media_type = scanner.Series
	case request.Media_type == "movie" || entry.Media_type == "movie":
		media_type = scanner.Movie
	}

	var result *matcher.Result
	switch {
	case request.Tmdb_id > 0:
		enriched, err := s.matcher.Enrich_by_tmdb(r.Context(), media_type, request.Tmdb_id)
		if err != nil {
			write_error(w, http.StatusBadGateway, err.Error())
			return
		}
		result = enriched
	case request.Imdb_id != "":
		enriched, err := s.matcher.Enrich_by_imdb(r.Context(), media_type, request.Imdb_id)
		if err != nil {
			write_error(w, http.StatusBadGateway, err.Error())
			return
		}
		result = enriched
	default:
		write_error(w, http.StatusBadRequest, "tmdb_id or imdb_id is required")
		return
	}

	updated := scan.Catalog_entry_from_match(result)
	updated.Id = id
	if err := s.store.Update_catalog_entry(r.Context(), id, updated); err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if result.Matched && s.matching != nil {
		if err := s.matching.Enrich_episodes(r.Context(), id, result); err != nil {
			write_error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	fresh, err := s.store.Get_catalog_entry(r.Context(), id)
	if err != nil || fresh == nil {
		write_error(w, http.StatusInternalServerError, "failed to reload catalog entry")
		return
	}
	write_json(w, http.StatusOK, map[string]any{"entry": catalog_item_from(*fresh)})
}

// handle_rematch re-runs the automatic matcher for one catalog entry and
// applies the result when the matcher is confident. When the match stays
// ambiguous it returns the candidate shortlist so the UI can offer a chooser.
func (s *Server) handle_rematch(w http.ResponseWriter, r *http.Request) {
	if s.matching == nil {
		write_error(w, http.StatusServiceUnavailable, "matching is not configured")
		return
	}
	id, ok := path_id(r)
	if !ok {
		write_error(w, http.StatusBadRequest, "invalid catalog id")
		return
	}
	applied, candidates, err := s.matching.Rematch_one(r.Context(), id)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if applied {
		fresh, err := s.store.Get_catalog_entry(r.Context(), id)
		if err != nil || fresh == nil {
			write_error(w, http.StatusInternalServerError, "failed to reload catalog entry")
			return
		}
		write_json(w, http.StatusOK, map[string]any{"applied": true, "entry": catalog_item_from(*fresh)})
		return
	}
	views := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		views = append(views, map[string]any{
			"tmdb_id":     c.Tmdb_id,
			"imdb_id":     c.Imdb_id,
			"title":       c.Title,
			"year":        c.Year,
			"score":       c.Score,
			"overview":    c.Overview,
			"poster_path": c.Poster_path,
			"media_type":  "movie",
		})
	}
	write_json(w, http.StatusOK, map[string]any{"applied": false, "candidates": views})
}
