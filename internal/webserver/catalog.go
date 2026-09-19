package webserver

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/cybersouris/tomovee/internal/database"
)

type catalog_item struct {
	Id              int64    `json:"id"`
	Media_type      string   `json:"media_type"`
	Title           string   `json:"title"`
	Original_title  string   `json:"original_title"`
	Release_year    int      `json:"release_year"`
	Overview        string   `json:"overview"`
	Runtime_minutes int      `json:"runtime_minutes"`
	Rating          float64  `json:"rating"`
	Vote_count      int      `json:"vote_count"`
	Imdb_id         string   `json:"imdb_id"`
	Tmdb_id         int      `json:"tmdb_id"`
	Poster_url      string   `json:"poster_url"`
	Status          string   `json:"status"`
	Genres          []string `json:"genres"`
}

type audio_item struct {
	Language string `json:"language"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Source   string `json:"source"`
}

type subtitle_item struct {
	Language string `json:"language"`
	Format   string `json:"format"`
	Source   string `json:"source"`
}

type version_item struct {
	Id                int64           `json:"id"`
	File_path         string          `json:"file_path"`
	File_url          string          `json:"file_url"`
	Size_bytes        int64           `json:"size_bytes"`
	Duration_seconds  float64         `json:"duration_seconds"`
	Container         string          `json:"container"`
	Resolution_width  int             `json:"resolution_width"`
	Resolution_height int             `json:"resolution_height"`
	Resolution_label  string          `json:"resolution_label"`
	Video_codec       string          `json:"video_codec"`
	Hdr               bool            `json:"hdr"`
	Frame_rate        float64         `json:"frame_rate"`
	Bit_depth         int             `json:"bit_depth"`
	Audio             []audio_item    `json:"audio"`
	Subtitles         []subtitle_item `json:"subtitles"`
}

type episode_item struct {
	Id             int64          `json:"id"`
	Season_number  int            `json:"season_number"`
	Episode_number int            `json:"episode_number"`
	Title          string         `json:"title"`
	Overview       string         `json:"overview"`
	Airdate        string         `json:"airdate"`
	Is_special     bool           `json:"is_special"`
	Status         string         `json:"status"`
	Versions       []version_item `json:"versions"`
}

type series_item struct {
	First_air_date string `json:"first_air_date"`
	Last_air_date  string `json:"last_air_date"`
	Num_seasons    int    `json:"num_seasons"`
	Num_episodes   int    `json:"num_episodes"`
}

type detail_response struct {
	Entry    catalog_item   `json:"entry"`
	Series   *series_item   `json:"series"`
	Episodes []episode_item `json:"episodes"`
	Versions []version_item `json:"versions"`
}

func (s *Server) handle_catalog_list(w http.ResponseWriter, r *http.Request) {
	filter := database.Catalog_filter{
		Media_type: r.URL.Query().Get("media_type"),
		Status:     r.URL.Query().Get("status"),
		Search:     r.URL.Query().Get("q"),
		Genre:      r.URL.Query().Get("genre"),
		Year:       int_query(r, "year"),
		Resolution: r.URL.Query().Get("resolution"),
		Language:   r.URL.Query().Get("language"),
		Sort:       r.URL.Query().Get("sort"),
		Desc:       r.URL.Query().Get("order") == "desc",
		Limit:      int_query(r, "limit"),
		Offset:     int_query(r, "offset"),
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	entries, err := s.store.List_catalog_entries(r.Context(), filter)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := s.store.Count_catalog_entries(r.Context(), filter)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	write_json(w, http.StatusOK, map[string]any{
		"total":   total,
		"count":   len(entries),
		"entries": catalog_items(entries),
	})
}

func (s *Server) handle_unmatched(w http.ResponseWriter, r *http.Request) {
	limit := int_query(r, "limit")
	offset := int_query(r, "offset")
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	filter := database.Catalog_filter{
		Status: "needs_lookup",
		Sort:   "added",
		Desc:   true,
		Limit:  limit,
		Offset: offset,
	}
	entries, err := s.store.List_catalog_entries(r.Context(), filter)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	total, err := s.store.Count_catalog_entries(r.Context(), filter)
	if err != nil {
		write_error(w, http.StatusInternalServerError, err.Error())
		return
	}
	write_json(w, http.StatusOK, map[string]any{
		"total":   total,
		"count":   len(entries),
		"entries": catalog_items(entries),
	})
}

func (s *Server) handle_catalog_detail(w http.ResponseWriter, r *http.Request) {
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

	response := detail_response{
		Entry:    catalog_item_from(*entry),
		Episodes: []episode_item{},
		Versions: []version_item{},
	}
	if entry.Media_type == "series" {
		if meta, err := s.store.Get_series_metadata(r.Context(), id); err == nil && meta != nil {
			response.Series = &series_item{
				First_air_date: meta.First_air_date, Last_air_date: meta.Last_air_date,
				Num_seasons: meta.Num_seasons, Num_episodes: meta.Num_episodes,
			}
		}
		episodes, err := s.store.List_episodes(r.Context(), id)
		if err != nil {
			write_error(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, episode := range episodes {
			versions, _ := s.store.List_versions_for_episode(r.Context(), episode.Id)
			response.Episodes = append(response.Episodes, episode_item{
				Id: episode.Id, Season_number: episode.Season_number, Episode_number: episode.Episode_number,
				Title: episode.Title, Overview: episode.Overview, Airdate: episode.Airdate,
				Is_special: episode.Is_special, Status: episode.Status,
				Versions: s.version_items(r.Context(), versions),
			})
		}
	} else {
		versions, err := s.store.List_versions_for_entry(r.Context(), id)
		if err != nil {
			write_error(w, http.StatusInternalServerError, err.Error())
			return
		}
		response.Versions = s.version_items(r.Context(), versions)
	}
	write_json(w, http.StatusOK, response)
}

func (s *Server) version_items(ctx context.Context, versions []database.Version) []version_item {
	roots := make(map[int64]string)
	if libraries, err := s.store.List_libraries(ctx); err == nil {
		for _, library := range libraries {
			roots[library.Id] = library.Path
		}
	}
	items := make([]version_item, 0, len(versions))
	for _, version := range versions {
		audio, subtitles, _ := s.store.Version_tracks(ctx, version.Id)
		display_path := version.File_path
		if root := roots[version.Library_id]; root != "" {
			display_path = filepath.Join(root, display_path)
		}
		item := version_item{
			Id: version.Id, File_path: display_path, File_url: "/api/v1/versions/" + itoa(version.Id) + "/file",
			Size_bytes:       version.Size_bytes,
			Duration_seconds: version.Duration_seconds, Container: version.Container,
			Resolution_width: version.Resolution_width, Resolution_height: version.Resolution_height,
			Resolution_label: version.Resolution_label, Video_codec: version.Video_codec,
			Hdr: version.Hdr, Frame_rate: version.Frame_rate, Bit_depth: version.Bit_depth,
			Audio:     []audio_item{},
			Subtitles: []subtitle_item{},
		}
		for _, track := range audio {
			item.Audio = append(item.Audio, audio_item{
				Language: track.Language, Codec: track.Codec, Channels: track.Channels, Source: track.Source,
			})
		}
		for _, track := range subtitles {
			item.Subtitles = append(item.Subtitles, subtitle_item{
				Language: track.Language, Format: track.Format, Source: track.Source,
			})
		}
		items = append(items, item)
	}
	return items
}

func catalog_items(entries []database.Catalog_entry) []catalog_item {
	items := make([]catalog_item, 0, len(entries))
	for _, entry := range entries {
		items = append(items, catalog_item_from(entry))
	}
	return items
}

func catalog_item_from(entry database.Catalog_entry) catalog_item {
	genres := entry.Genres
	if genres == nil {
		genres = []string{}
	}
	item := catalog_item{
		Id: entry.Id, Media_type: entry.Media_type, Title: entry.Title,
		Original_title: entry.Original_title, Release_year: entry.Release_year,
		Overview: entry.Overview, Runtime_minutes: entry.Runtime_minutes,
		Rating: entry.Rating, Vote_count: entry.Vote_count, Imdb_id: entry.Imdb_id,
		Tmdb_id: entry.Tmdb_id, Status: entry.Status, Genres: genres,
	}
	if entry.Poster_path != "" {
		item.Poster_url = "/api/v1/posters/" + itoa(entry.Id)
	}
	return item
}
