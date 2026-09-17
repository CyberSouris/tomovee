package scan

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/cybersouris/tomovee/internal/database"
	"github.com/cybersouris/tomovee/internal/matcher"
	"github.com/cybersouris/tomovee/internal/metadata"
	"github.com/cybersouris/tomovee/internal/scanner"
)

// persist groups the file's match under a catalog entry, creates the episode
// row when applicable, and writes the version with its tracks.
func (s *Scanner) persist(ctx context.Context, file scanner.Found_file, info *metadata.File_info, match *matcher.Result) error {
	media_type := media_type_string(file.Media_type)
	if match.Matched {
		media_type = media_type_string(match.Media_type)
	}

	entry_id, err := s.store.Upsert_catalog_entry(ctx, catalog_entry_from(file, match, media_type))
	if err != nil {
		return err
	}

	if media_type == "series" {
		if err := s.store.Upsert_series_metadata(ctx, database.Series_metadata{
			Catalog_entry_id: entry_id,
			First_air_date:   match.First_air_date,
			Last_air_date:    match.Last_air_date,
			Num_seasons:      match.Number_of_seasons,
			Num_episodes:     match.Number_of_episodes,
		}); err != nil {
			return err
		}
	}

	version := version_from(file, info)
	if media_type == "series" && file.Episode != nil {
		episode_id, err := s.store.Upsert_episode(ctx, episode_from(entry_id, file, match))
		if err != nil {
			return err
		}
		version.Episode_id = episode_id
	} else {
		version.Catalog_entry_id = entry_id
	}
	_, err = s.store.Save_version(ctx, version)
	return err
}

// Catalog_entry_from_match converts a matcher result into a catalog entry,
// deriving its status from whether the match succeeded. It is exported for the
// web API's manual re-matching.
func Catalog_entry_from_match(match *matcher.Result) database.Catalog_entry {
	status := "needs_lookup"
	if match.Matched {
		status = "matched"
	}
	return database.Catalog_entry{
		Media_type:      media_type_string(match.Media_type),
		Title:           match.Title,
		Original_title:  match.Original_title,
		Release_year:    match.Year,
		Overview:        match.Overview,
		Runtime_minutes: match.Runtime_minutes,
		Rating:          match.Rating,
		Vote_count:      match.Vote_count,
		Imdb_id:         match.Imdb_id,
		Tmdb_id:         match.Tmdb_id,
		Poster_path:     match.Poster_path,
		Status:          status,
		Genres:          match.Genres,
	}
}

func catalog_entry_from(file scanner.Found_file, match *matcher.Result, media_type string) database.Catalog_entry {
	entry := Catalog_entry_from_match(match)
	entry.Media_type = media_type
	if entry.Title == "" {
		entry.Title = strings.TrimSuffix(file.Name, filepath.Ext(file.Name))
	}
	return entry
}

func episode_from(entry_id int64, file scanner.Found_file, match *matcher.Result) database.Episode {
	status := "needs_lookup"
	if match.Matched {
		status = "matched"
	}
	return database.Episode{
		Catalog_entry_id: entry_id,
		Season_number:    file.Episode.Season,
		Episode_number:   file.Episode.Episode,
		Is_special:       file.Is_special,
		Status:           status,
	}
}

func version_from(file scanner.Found_file, info *metadata.File_info) database.Version {
	version := database.Version{
		File_path:        file.Path,
		Size_bytes:       file.Size_bytes,
		Mtime:            format_mtime(file.Mtime),
		Duration_seconds: info.Duration_seconds,
		Container:        info.Container,
	}
	if info.Video != nil {
		version.Resolution_width = info.Video.Width
		version.Resolution_height = info.Video.Height
		version.Resolution_label = info.Video.Resolution_label
		version.Video_codec = info.Video.Codec
		version.Hdr = info.Video.Hdr
		version.Frame_rate = info.Video.Frame_rate
		version.Bit_depth = info.Video.Bit_depth
	}
	for _, track := range info.Audio {
		version.Audio = append(version.Audio, database.Audio_track{
			Language: track.Language,
			Codec:    track.Codec,
			Channels: track.Channels,
			Source:   "ffprobe",
		})
	}
	for _, track := range info.Subtitles {
		version.Subtitles = append(version.Subtitles, database.Subtitle_track{
			Language: track.Language,
			Format:   track.Format,
			Source:   "ffprobe",
		})
	}
	return version
}

func media_type_string(media_type scanner.Media_type) string {
	if media_type == scanner.Series {
		return "series"
	}
	return "movie"
}
