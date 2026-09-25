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

// reclassify_request carries the media type a user wants a catalog entry forced
// to. Reclassify flips series<->movie (or the reverse) rather than re-guessing,
// which is how a user fixes an entry the scanner typed wrong.
type reclassify_request struct {
	Media_type string `json:"media_type"`
}

// handle_manual_match associates an unmatched catalog entry with the TMDB or
// IMDb identifier chosen by the user. When no TMDB API key is configured it
// falls back to the local IMDb index, so picking an offline candidate still
// works.
func (s *Server) handle_manual_match(w http.ResponseWriter, r *http.Request) {
	if s.matcher == nil {
		write_error(w, http.StatusServiceUnavailable, "matcher is not configured")
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
		s.internal_error(w, err, "load catalog entry")
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
		if s.metadata != nil {
			enriched, err := s.matcher.Enrich_by_tmdb(r.Context(), media_type, request.Tmdb_id)
			if err != nil {
				s.bad_gateway(w, err, "enrich from tmdb")
				return
			}
			result = enriched
		} else if resolved := s.offline_from_candidate(r.Context(), id, request.Tmdb_id, ""); resolved != nil {
			result = resolved
		} else {
			write_error(w, http.StatusServiceUnavailable, "matching by TMDB id requires a TMDB API key")
			return
		}
	case request.Imdb_id != "":
		if s.metadata != nil {
			enriched, err := s.matcher.Enrich_by_imdb(r.Context(), media_type, request.Imdb_id)
			if err == nil {
				result = enriched
				break
			}
		}
		if resolved, ok := s.matcher.Resolve_by_imdb_offline(r.Context(), request.Imdb_id); ok {
			result = resolved
			break
		}
		if s.metadata == nil {
			write_error(w, http.StatusServiceUnavailable, "matching by IMDb id requires a TMDB API key or a local IMDb index entry")
			return
		}
		write_error(w, http.StatusBadGateway, "could not resolve IMDb id "+request.Imdb_id)
		return
	default:
		write_error(w, http.StatusBadRequest, "tmdb_id or imdb_id is required")
		return
	}

	updated := scan.Catalog_entry_from_match(result)
	updated.Id = id
	if err := s.store.Update_catalog_entry(r.Context(), id, updated); err != nil {
		s.internal_error(w, err, "update catalog entry")
		return
	}
	if result.Matched && s.matching != nil {
		if err := s.matching.Enrich_episodes(r.Context(), id, result); err != nil {
			s.internal_error(w, err, "enrich episodes")
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

// offline_from_candidate resolves a picked candidate against the persisted
// shortlist when TMDB is unavailable: it finds the stored candidate whose
// tmdb_id (or imdb_id) matches and resolves its imdb id through the local
// index. A nil result means the id is not in the shortlist or not resolvable.
func (s *Server) offline_from_candidate(ctx context.Context, entry_id int64, tmdb_id int, imdb_id string) *matcher.Result {
	stored, err := s.store.List_candidates(ctx, entry_id)
	if err != nil {
		return nil
	}
	for _, c := range stored {
		if tmdb_id > 0 && c.Tmdb_id != tmdb_id {
			continue
		}
		if imdb_id != "" && c.Imdb_id != imdb_id {
			continue
		}
		if c.Imdb_id == "" {
			continue
		}
		if result, ok := s.matcher.Resolve_by_imdb_offline(ctx, c.Imdb_id); ok {
			return result
		}
	}
	return nil
}

// handle_rematch re-runs the automatic matcher for one catalog entry through
// the same job manager as a full matching run, so the rematch shows up in the
// global background status and SSE feed. When a matching job is already in
// progress the rematch is queued behind it instead of being rejected, so the
// entry is still processed as soon as the current pass finishes. When the
// matcher applies a match it clears any stored candidates; when the match
// stays ambiguous it persists the candidate shortlist for the entry so both
// the Unmatched screen and the title detail page can offer a chooser.
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
		s.internal_error(w, err, "load catalog entry")
		return
	}
	if entry == nil {
		write_error(w, http.StatusNotFound, "catalog entry not found")
		return
	}

	var applied bool
	var candidates []matcher.Candidate
	_, err = s.matches.Run_sync_queued(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
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
		write_error(w, http.StatusConflict, job_running_error("a matching job is already running"))
		return
	}
	if err != nil {
		s.internal_error(w, err, "run matching job")
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

// handle_reclassify forces a catalog entry to the opposite media type the
// scanner gave it (series<->movie or movie<->series) and re-runs automatic
// matching for it under the flipped classification through the same job manager
// as a background pass, so the correction shows up in the global status and SSE
// feed exactly like a rematch does. A confident flip persists the re-typed
// entry (and re-enriches a series with fresh episode titles); an ambiguous one
// persists the flipped entry's candidate shortlist for the user to choose from.
func (s *Server) handle_reclassify(w http.ResponseWriter, r *http.Request) {
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
		s.internal_error(w, err, "load catalog entry")
		return
	}
	if entry == nil {
		write_error(w, http.StatusNotFound, "catalog entry not found")
		return
	}

	var request reclassify_request
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		write_error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	media_type := request.Media_type
	if media_type != "series" && media_type != "movie" {
		write_error(w, http.StatusBadRequest, "media_type must be series or movie")
		return
	}
	as_series := media_type == "series"

	var applied bool
	var candidates []matcher.Candidate
	var result_id int64
	_, err = s.matches.Run_sync_queued(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		var run_err error
		result_id, applied, candidates, run_err = s.matching.Reclassify(ctx, id, as_series)
		if run_err != nil {
			return nil, run_err
		}
		result := &matching.Result{Total: 1}
		if applied {
			result.Matched = 1
			if err := s.store.Clear_candidates(ctx, result_id); err != nil {
				return nil, err
			}
		} else {
			result.Unmatched = 1
			result.Candidates = len(candidates)
			if err := s.store.Replace_candidates(ctx, result_id, candidates_to_db(media_type, candidates)); err != nil {
				return nil, err
			}
			if len(candidates) > 0 && entry.Status != "needs_lookup" {
				if err := s.store.Set_catalog_status(ctx, result_id, "needs_lookup"); err != nil {
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
		write_error(w, http.StatusConflict, job_running_error("a matching job is already running"))
		return
	}
	if err != nil {
		s.internal_error(w, err, "run matching job")
		return
	}

	if applied {
		fresh, err := s.store.Get_catalog_entry(r.Context(), result_id)
		if err != nil || fresh == nil {
			write_error(w, http.StatusInternalServerError, "failed to reload catalog entry")
			return
		}
		write_json(w, http.StatusOK, map[string]any{"applied": true, "entry": catalog_item_from(*fresh)})
		return
	}
	views := candidate_items(candidates, media_type)
	body := map[string]any{"applied": false, "candidates": views}
	// When the re-typed entry was absorbed into an existing series it no longer
	// exists, so the UI needs the surviving entry to follow.
	if result_id != id {
		if fresh, err := s.store.Get_catalog_entry(r.Context(), result_id); err == nil && fresh != nil {
			body["entry"] = catalog_item_from(*fresh)
		}
	}
	write_json(w, http.StatusOK, body)
}
