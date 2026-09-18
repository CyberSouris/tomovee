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

// persist groups the file under a catalog entry, creates the episode row when
// applicable, and writes the version with its stored hash. File paths are
// stored relative to the library's root. Matching is a separate, later step,
// so entries are persisted as "needs_lookup"; entries that are already matched
// keep their richer metadata untouched.
func (s *Scanner) persist(ctx context.Context, library database.Library, root string, file scanner.Found_file, info *metadata.File_info, hash string) error {
	relative, err := filepath.Rel(root, file.Path)
	if err != nil {
		return err
	}

	media_type := media_type_string(file.Media_type)
	entry := catalog_entry_from(file, media_type)

	entry_id, err := s.entry_id(ctx, entry)
	if err != nil {
		return err
	}

	version := version_from(file, info)
	version.Hash = hash
	version.Library_id = library.Id
	version.File_path = relative
	if media_type == "series" && file.Episode != nil {
		episode_id, err := s.store.Upsert_episode(ctx, episode_from(entry_id, file))
		if err != nil {
			return err
		}
		version.Episode_id = episode_id
	} else {
		version.Catalog_entry_id = entry_id
	}
	_, err = s.store.Save_version(ctx, version)
	if err != nil {
		return err
	}
	s.ensure_frame_poster(ctx, entry_id, file.Path, info.Duration_seconds)
	return nil
}

// entry_id returns the catalog entry the file belongs to. Unmatched entries
// (and new groups) are upserted from the parsed filename; already-matched
// entries keep their existing row so a re-scan does not downgrade them.
func (s *Scanner) entry_id(ctx context.Context, entry database.Catalog_entry) (int64, error) {
	existing_id, found, err := s.store.Find_catalog_entry(ctx, entry)
	if err != nil {
		return 0, err
	}
	if found {
		existing, err := s.store.Get_catalog_entry(ctx, existing_id)
		if err != nil {
			return 0, err
		}
		if existing != nil && existing.Status == "matched" {
			return existing.Id, nil
		}
	}
	return s.store.Upsert_catalog_entry(ctx, entry)
}

// Catalog_entry_from_match converts a matcher result into a catalog entry,
// deriving its status from whether the match succeeded. It is exported for the
// web API's matching pipeline.
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

// catalog_entry_from derives the entry stored during an offline scan from the
// parsed file name. The media type is taken from the scanner's classification.
func catalog_entry_from(file scanner.Found_file, media_type string) database.Catalog_entry {
	hint := matcher.Parse_filename(file.Name)
	title := hint.Title
	if title == "" {
		title = strings.TrimSuffix(file.Name, filepath.Ext(file.Name))
	}
	return database.Catalog_entry{
		Media_type:   media_type,
		Title:        title,
		Release_year: hint.Year,
		Status:       "needs_lookup",
	}
}

func episode_from(entry_id int64, file scanner.Found_file) database.Episode {
	return database.Episode{
		Catalog_entry_id: entry_id,
		Season_number:    file.Episode.Season,
		Episode_number:   file.Episode.Episode,
		Is_special:       file.Is_special,
		Status:           "needs_lookup",
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
