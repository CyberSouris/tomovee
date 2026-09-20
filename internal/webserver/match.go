package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/matching"
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

// handle_rematch re-runs the automatic matcher for one catalog entry through
// the same job manager as a full matching run, so the rematch shows up in the
// global background status and SSE feed. When the matcher applies a match it
// clears any stored candidates; when the match stays ambiguous it persists the
// candidate shortlist for the entry so both the Unmatched screen and the title
// detail page can offer a chooser.
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

	entry, err := s.store.Get_catalog_entry(r.Context(), id)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entry == nil {
		write_error(w, http.StatusNotFound, "catalog entry not found")
		return
	}

	var applied bool
	var candidates []matcher.Candidate
	_, err = s.matches.Run_sync(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		var run_err error
		applied, candidates, run_err = s.matching.Rematch_one(ctx, id)
		if run_err != nil {
			return nil, run_err
		}
		result := &matching.Result{Total: 1}
		if applied {
			result.Matched = 1
			if err := s.store.Clear_candidates(ctx, id); err != nil {
				return nil, err
			}
		} else {
			result.Unmatched = 1
			result.Candidates = len(candidates)
			if err := s.store.Replace_candidates(ctx, id, candidates_to_db(entry.Media_type, candidates)); err != nil {
				return nil, err
			}
			if len(candidates) > 0 && entry.Status != "needs_lookup" {
				if err := s.store.Set_catalog_status(ctx, id, "needs_lookup"); err != nil {
					return nil, err
				}
			}
		}
		if progress != nil {
			progress(matching.Progress{
				Phase: "file", Title: entry.Title, Total: 1, Done: 1,
				Matched: result.Matched, Unmatched: result.Unmatched,
			})
		}
		return result, nil
	})
	if errors.Is(err, Err_job_running) {
		write_error(w, http.StatusConflict, job_running_error("a matching job is already running", err))
		return
	}
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
	views := candidate_items(candidates, entry.Media_type)
	write_json(w, http.StatusOK, map[string]any{"applied": false, "candidates": views})
}

// candidates_to_db maps matcher candidates (which carry no media type) onto
// persisted candidate rows tagged with the entry's media type.
func candidates_to_db(media_type string, candidates []matcher.Candidate) []database.Candidate {
	rows := make([]database.Candidate, 0, len(candidates))
	for _, c := range candidates {
		rows = append(rows, database.Candidate{
			Tmdb_id: c.Tmdb_id, Imdb_id: c.Imdb_id, Title: c.Title, Year: c.Year,
			Media_type: media_type, Score: c.Score, Overview: c.Overview, Poster_path: c.Poster_path,
		})
	}
	return rows
}
