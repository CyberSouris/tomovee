package webserver

import (
	"net/http"
	"strconv"
	"strings"
)

// search_item is one autocomplete suggestion for manual matching.
type search_item struct {
	Tmdb_id      int     `json:"tmdb_id"`
	Media_type   string  `json:"media_type"`
	Title        string  `json:"title"`
	Year         int     `json:"year"`
	Overview     string  `json:"overview"`
	Poster_url   string  `json:"poster_url"`
	Vote_average float64 `json:"vote_average"`
}

// max_search_results caps how many suggestions the autocomplete returns.
const max_search_results = 8

// handle_search surfaces TMDB candidates for the manual-match autocomplete.
// It matches by title and optionally a year, and is restricted to one media
// type at a time.
func (s *Server) handle_search(w http.ResponseWriter, r *http.Request) {
	if s.metadata == nil {
		write_error(w, http.StatusServiceUnavailable, "search unavailable: no TMDB API key configured")
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
	if media_type == "series" {
		results, err := s.metadata.Search_tv(ctx, query, year)
		if err != nil {
			write_error(w, http.StatusBadGateway, err.Error())
			return
		}
		items := make([]search_item, 0, min(len(results), max_search_results))
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
		write_json(w, http.StatusOK, map[string]any{"results": items})
		return
	}

	results, err := s.metadata.Search_movie(ctx, query, year)
	if err != nil {
		write_error(w, http.StatusBadGateway, err.Error())
		return
	}
	items := make([]search_item, 0, min(len(results), max_search_results))
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
	write_json(w, http.StatusOK, map[string]any{"results": items})
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
