package webserver

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/cybersouris/tomovee/internal/scanner"
)

// search_item is one autocomplete suggestion for manual matching. A candidate
// comes from either TMDB (Tmdb_id set) or the local IMDb index (Imdb_id set);
// a result can also carry both when the local entry is already linked to TMDB.
type search_item struct {
	Tmdb_id      int     `json:"tmdb_id"`
	Imdb_id      string  `json:"imdb_id,omitempty"`
	Media_type   string  `json:"media_type"`
	Title        string  `json:"title"`
	Year         int     `json:"year"`
	Overview     string  `json:"overview"`
	Poster_url   string  `json:"poster_url"`
	Vote_average float64 `json:"vote_average"`
}

// max_search_results caps how many suggestions the autocomplete returns.
const max_search_results = 8

// handle_search surfaces autocomplete candidates for the manual-match search
// box. It queries TMDB (when configured) and always augments the results with
// matches from the local IMDb index (when attached), so searching works even
// with no TMDB connection. It matches by title and optionally a year, and is
// restricted to one media type at a time.
func (s *Server) handle_search(w http.ResponseWriter, r *http.Request) {
	local_ok := s.matcher != nil && s.matcher.Has_local_search()
	if s.metadata == nil && !local_ok {
		write_error(w, http.StatusServiceUnavailable, "search unavailable: no TMDB API key and no local IMDb index configured")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		write_error(w, http.StatusBadRequest, "q query parameter is required")
		return
	}
	media_type := r.URL.Query().Get("media_type")
	if media_type != "series" {
		media_type = "movie"
	}
	year := int_query(r, "year")

	ctx := r.Context()
	items := make([]search_item, 0, max_search_results)

	if s.metadata != nil {
		if media_type == "series" {
			results, err := s.metadata.Search_tv(ctx, query, year)
			if err != nil {
				write_error(w, http.StatusBadGateway, err.Error())
				return
			}
			for _, res := range results {
				if len(items) >= max_search_results {
					break
				}
				items = append(items, search_item{
					Tmdb_id: res.Id, Media_type: "series", Title: res.Name,
					Year: year_of(res.First_air_date), Overview: res.Overview,
					Poster_url: tmdb_poster_url(res.Poster_path), Vote_average: res.Vote_average,
				})
			}
		} else {
			results, err := s.metadata.Search_movie(ctx, query, year)
			if err != nil {
				write_error(w, http.StatusBadGateway, err.Error())
				return
			}
			for _, res := range results {
				if len(items) >= max_search_results {
					break
				}
				items = append(items, search_item{
					Tmdb_id: res.Id, Media_type: "movie", Title: res.Title,
					Year: year_of(res.Release_date), Overview: res.Overview,
					Poster_url: tmdb_poster_url(res.Poster_path), Vote_average: res.Vote_average,
				})
			}
		}
	}

	if local_ok {
		media := scanner.Movie
		if media_type == "series" {
			media = scanner.Series
		}
		results, err := s.matcher.Search_local_autocomplete(ctx, query, year, media, max_search_results)
		if err != nil {
			write_error(w, http.StatusBadGateway, err.Error())
			return
		}
		for _, cand := range results {
			if len(items) >= max_search_results {
				break
			}
			if search_item_has(items, cand.Title, cand.Year) {
				continue
			}
			items = append(items, search_item{
				Imdb_id: cand.Imdb_id, Media_type: media_type, Title: cand.Title, Year: cand.Year,
			})
		}
	}

	write_json(w, http.StatusOK, map[string]any{"results": items})
}

// search_item_has reports whether items already carries a suggestion with the
// same title and year, so a local candidate that duplicates a TMDB hit for an
// already-linked filing is shown only once.
func search_item_has(items []search_item, title string, year int) bool {
	for _, it := range items {
		if it.Title == title && it.Year == year {
			return true
		}
	}
	return false
}

// year_of extracts the year from a "2001-01-01" style release or air date.
func year_of(date string) int {
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil || year <= 0 {
		return 0
	}
	return year
}

// tmdb_poster_url builds the small poster thumbnail used in suggestions.
func tmdb_poster_url(path string) string {
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/w92" + path
}
