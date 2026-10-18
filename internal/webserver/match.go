package webserver

import (
	"context"
	"encoding/json"
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

	entry, ok := s.load_catalog_entry(r.Context(), w, id)
	if !ok {
		return
	}

	media_type := scanner.Movie
	switch {
	case request.Media_type == "series" || entry.Media_type == "series":
		media_type = scanner.Series
	case request.Media_type == "movie" || entry.Media_type == "movie":
		media_type = scanner.Movie
	}

	result, ok := s.resolve_manual_candidate(r.Context(), w, id, media_type, request)
	if !ok {
		return
	}
	if !s.apply_manual_match(r.Context(), w, id, result) {
		return
	}
	fresh, err := s.store.Get_catalog_entry(r.Context(), id)
	if err != nil || fresh == nil {
		write_error(w, http.StatusInternalServerError, "failed to reload catalog entry")
		return
	}
	write_json(w, http.StatusOK, map[string]any{"entry": catalog_item_from(*fresh)})
}

// resolve_manual_candidate turns the identifier a user picked into a matcher
// result: TMDB when a key is configured, the persisted offline shortlist as
// fallback. It writes the failure response itself and returns ok=false when
// the identifier cannot be resolved.
func (s *Server) resolve_manual_candidate(ctx context.Context, w http.ResponseWriter, entry_id int64, media_type scanner.Media_type, request match_request) (*matcher.Result, bool) {
	switch {
	case request.Tmdb_id > 0:
		if s.metadata != nil {
			enriched, err := s.matcher.Enrich_by_tmdb(ctx, media_type, request.Tmdb_id)
			if err != nil {
				s.bad_gateway(w, err, "enrich from tmdb")
				return nil, false
			}
			return enriched, true
		}
		if resolved := s.offline_from_candidate(ctx, entry_id, request.Tmdb_id, ""); resolved != nil {
			return resolved, true
		}
		write_error(w, http.StatusServiceUnavailable, "matching by TMDB id requires a TMDB API key")
		return nil, false
	case request.Imdb_id != "":
		if s.metadata != nil {
			if enriched, err := s.matcher.Enrich_by_imdb(ctx, media_type, request.Imdb_id); err == nil {
				return enriched, true
			}
		}
		if resolved, ok := s.matcher.Resolve_by_imdb_offline(ctx, request.Imdb_id); ok {
			return resolved, true
		}
		if s.metadata == nil {
			write_error(w, http.StatusServiceUnavailable, "matching by IMDb id requires a TMDB API key or a local IMDb index entry")
			return nil, false
		}
		write_error(w, http.StatusBadGateway, "could not resolve IMDb id "+request.Imdb_id)
		return nil, false
	default:
		write_error(w, http.StatusBadRequest, "tmdb_id or imdb_id is required")
		return nil, false
	}
}

// apply_manual_match stores the resolved match on the entry and re-enriches
// episode titles when the match was confident. It writes the failure response
// itself and returns ok=false on a storage failure.
func (s *Server) apply_manual_match(ctx context.Context, w http.ResponseWriter, id int64, result *matcher.Result) bool {
	updated := scan.Catalog_entry_from_match(result)
	updated.Id = id
	if err := s.store.Update_catalog_entry(ctx, id, updated); err != nil {
		s.internal_error(w, err, "update catalog entry")
		return false
	}
	if result.Matched && s.matching != nil {
		if err := s.matching.Enrich_episodes(ctx, id, result); err != nil {
			s.internal_error(w, err, "enrich episodes")
			return false
		}
	}
	return true
}

// load_catalog_entry loads a catalog entry, writing the failure response
// itself when it cannot be loaded.
func (s *Server) load_catalog_entry(ctx context.Context, w http.ResponseWriter, id int64) (*database.Catalog_entry, bool) {
	entry, err := s.store.Get_catalog_entry(ctx, id)
	if err != nil {
		s.internal_error(w, err, "load catalog entry")
		return nil, false
	}
	if entry == nil {
		write_error(w, http.StatusNotFound, "catalog entry not found")
		return nil, false
	}
	return entry, true
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

// record_match_outcome persists what a single-entry match attempt produced:
// an applied match clears the stored candidates, an ambiguous one replaces the
// candidate shortlist (and flags the entry for manual lookup). It also builds
// the job's result and reports the final progress tick.
func (s *Server) record_match_outcome(ctx context.Context, progress func(matching.Progress), entry_id int64, media_type string, prev_status string, title string, applied bool, candidates []matcher.Candidate) (*matching.Result, error) {
	result := &matching.Result{Total: 1}
	if applied {
		result.Matched = 1
		if err := s.store.Clear_candidates(ctx, entry_id); err != nil {
			return nil, err
		}
	} else {
		result.Unmatched = 1
		result.Candidates = len(candidates)
		if err := s.store.Replace_candidates(ctx, entry_id, candidates_to_db(media_type, candidates)); err != nil {
			return nil, err
		}
		if len(candidates) > 0 && prev_status != "needs_lookup" {
			if err := s.store.Set_catalog_status(ctx, entry_id, "needs_lookup"); err != nil {
				return nil, err
			}
		}
	}
	if progress != nil {
		progress(matching.Progress{
			Phase: "file", Title: title, Total: 1, Done: 1,
			Matched: result.Matched, Unmatched: result.Unmatched,
		})
	}
	return result, nil
}

// write_applied_or_candidates answers a single-entry match request: an
// applied match returns the reloaded entry, an ambiguous one the candidate
// shortlist (plus the surviving entry when the re-typed entry was absorbed).
func (s *Server) write_applied_or_candidates(ctx context.Context, w http.ResponseWriter, entry_id int64, absorbed_id int64, media_type string, applied bool, candidates []matcher.Candidate) {
	if applied {
		fresh, err := s.store.Get_catalog_entry(ctx, entry_id)
		if err != nil || fresh == nil {
			write_error(w, http.StatusInternalServerError, "failed to reload catalog entry")
			return
		}
		write_json(w, http.StatusOK, map[string]any{"applied": true, "entry": catalog_item_from(*fresh)})
		return
	}
	body := map[string]any{"applied": false, "candidates": candidate_items(candidates, media_type)}
	// When the re-typed entry was absorbed into an existing series it no longer
	// exists, so the UI needs the surviving entry to follow.
	if absorbed_id != 0 && absorbed_id != entry_id {
		if fresh, err := s.store.Get_catalog_entry(ctx, absorbed_id); err == nil && fresh != nil {
			body["entry"] = catalog_item_from(*fresh)
		}
	}
	write_json(w, http.StatusOK, body)
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
	entry, ok := s.load_catalog_entry(r.Context(), w, id)
	if !ok {
		return
	}

	applied, candidates, err := s.run_rematch_one(id, entry)
	if err != nil {
		s.internal_error(w, err, "run matching job")
		return
	}
	s.write_applied_or_candidates(r.Context(), w, id, 0, entry.Media_type, applied, candidates)
}

// run_rematch_one runs a single-entry rematch through the shared job manager.
func (s *Server) run_rematch_one(id int64, entry *database.Catalog_entry) (bool, []matcher.Candidate, error) {
	var applied bool
	var candidates []matcher.Candidate
	_, err := s.matches.Run_sync_queued(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		var run_err error
		applied, candidates, run_err = s.matching.Rematch_one(ctx, id)
		if run_err != nil {
			return nil, run_err
		}
		return s.record_match_outcome(ctx, progress, id, entry.Media_type, entry.Status, entry.Title, applied, candidates)
	})
	return applied, candidates, err
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

// handle_version_episode attaches a stored file to the episode a user chose for
// it. The season and episode numbers come from the request body, so files whose
// names did not parse as episodes find their place by hand. The owning entry
// must be a series; the episode row is created on demand.
func (s *Server) handle_version_episode(w http.ResponseWriter, r *http.Request) {
	if s.matching == nil {
		write_error(w, http.StatusServiceUnavailable, "matching is not configured")
		return
	}
	version, ok := s.load_version_or_error(w, r)
	if !ok {
		return
	}
	var request episode_assign_request
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		write_error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if request.Season < 0 || request.Episode <= 0 {
		write_error(w, http.StatusBadRequest, "season must be >= 0 and episode must be >= 1")
		return
	}
	episode_id, err := s.matching.Assign_version_to_episode(r.Context(), version.Id, request.Season, request.Episode)
	if err != nil {
		s.internal_error(w, err, "assign version to episode")
		return
	}
	if episode_id == 0 {
		write_error(w, http.StatusNotFound, "version not found")
		return
	}
	write_json(w, http.StatusOK, map[string]any{"ok": true, "episode_id": episode_id})
}

// episode_assign_request carries the target episode a user picks for a stored
// file on the series detail page.
type episode_assign_request struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

// handle_version_split cuts one stored file out of its current entry and turns
// it into a movie of its own. It runs through the same job manager as a rematch
// so the newly created movie is immediately matched and the correction shows up
// in the global status and SSE feed. The response carries the new entry, like a
// manual match response, so the UI can follow it.
func (s *Server) handle_version_split(w http.ResponseWriter, r *http.Request) {
	if s.matching == nil {
		write_error(w, http.StatusServiceUnavailable, "matching is not configured")
		return
	}
	version, ok := s.load_version_or_error(w, r)
	if !ok {
		return
	}

	new_id, applied, candidates, err := s.run_split_version(version.Id)
	if err != nil {
		s.internal_error(w, err, "run matching job")
		return
	}
	if new_id == 0 {
		write_error(w, http.StatusNotFound, "version not found")
		return
	}
	fresh, err := s.store.Get_catalog_entry(r.Context(), new_id)
	if err != nil || fresh == nil {
		write_error(w, http.StatusInternalServerError, "failed to reload catalog entry")
		return
	}
	write_json(w, http.StatusOK, map[string]any{
		"applied":    applied,
		"entry":      catalog_item_from(*fresh),
		"candidates": candidate_items(candidates, "movie"),
	})
}

// run_split_version splits a version into its own movie entry and rematches
// the new entry, all inside one queued matching job.
func (s *Server) run_split_version(version_id int64) (int64, bool, []matcher.Candidate, error) {
	var applied bool
	var candidates []matcher.Candidate
	var new_id int64
	_, err := s.matches.Run_sync_queued(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		var run_err error
		new_id, run_err = s.matching.Split_version_to_movie(ctx, version_id)
		if run_err != nil {
			return nil, run_err
		}
		if new_id == 0 {
			return nil, nil
		}
		title := ""
		fresh, get_err := s.store.Get_catalog_entry(ctx, new_id)
		if get_err != nil {
			return nil, get_err
		}
		if fresh != nil {
			title = fresh.Title
			applied, candidates, run_err = s.matching.Rematch_one(ctx, new_id)
			if run_err != nil {
				return nil, run_err
			}
		}
		return s.record_match_outcome(ctx, progress, new_id, "movie", "needs_lookup", title, applied, candidates)
	})
	return new_id, applied, candidates, err
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
	entry, ok := s.load_catalog_entry(r.Context(), w, id)
	if !ok {
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

	result_id, applied, candidates, err := s.run_reclassify(id, entry, as_series, media_type)
	if err != nil {
		s.internal_error(w, err, "run matching job")
		return
	}
	s.write_applied_or_candidates(r.Context(), w, result_id, id, media_type, applied, candidates)
}

// run_reclassify flips an entry's media type and rematches it under the new
// classification, all inside one queued matching job.
func (s *Server) run_reclassify(id int64, entry *database.Catalog_entry, as_series bool, media_type string) (int64, bool, []matcher.Candidate, error) {
	var applied bool
	var candidates []matcher.Candidate
	var result_id int64
	_, err := s.matches.Run_sync_queued(func(ctx context.Context, progress func(matching.Progress)) (*matching.Result, error) {
		var run_err error
		result_id, applied, candidates, run_err = s.matching.Reclassify(ctx, id, as_series)
		if run_err != nil {
			return nil, run_err
		}
		return s.record_match_outcome(ctx, progress, result_id, media_type, entry.Status, entry.Title, applied, candidates)
	})
	return result_id, applied, candidates, err
}
