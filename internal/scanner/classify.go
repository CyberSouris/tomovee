// Package scanner discovers media files on disk and classifies them as movie,
// series episode, or junk. It is the front of the scanning pipeline.
package scanner

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Media_kind describes what a discovered file is.
type Media_kind int

const (
	// Video is a regular media file to be catalogued.
	Video Media_kind = iota
	// Junk is a file that should be skipped (sample, trailer, extras, ...).
	Junk
	// Not_video is a file with an extension outside the accepted set.
	Not_video
)

// Media_type distinguishes movies from series episodes.
type Media_type int

const (
	Movie Media_type = iota
	Series
)

// Episode_hint carries series season/episode information parsed from a
// filename, together with the cleaned show name.
type Episode_hint struct {
	Show_name  string
	Season     int
	Episode    int
	Is_special bool
}

// Classified is the result of classifying a single file.
type Classified struct {
	Kind    Media_kind
	Type    Media_type
	Episode *Episode_hint
}

// Video_extensions is the accepted set of video container extensions,
// lower-case without the leading dot.
var Video_extensions = map[string]bool{
	"mkv": true, "mp4": true, "avi": true, "mov": true, "webm": true,
	"ts": true, "m2ts": true, "mts": true, "wmv": true, "flv": true,
	"m4v": true, "mpg": true, "mpeg": true, "vob": true, "ogv": true,
	"divx": true,
}

// Is_video_file reports whether name has an accepted video extension.
func Is_video_file(name string) bool {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	return Video_extensions[ext]
}

// junk_patterns matches filenames that surely are not content: samples,
// trailers, extras, featurettes, and similar non-content material.
var junk_patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsample\b`),
	regexp.MustCompile(`(?i)\btrailer\b`),
	regexp.MustCompile(`(?i)\bextras?\b`),
	regexp.MustCompile(`(?i)\bfeaturette\b`),
	regexp.MustCompile(`(?i)\bbehind[ _-]?the[ _-]?scenes\b`),
	regexp.MustCompile(`(?i)\bbloopers?\b`),
	regexp.MustCompile(`(?i)\bintro\b`),
	regexp.MustCompile(`(?i)\bcredits?\b`),
	regexp.MustCompile(`(?i)\bteaser\b`),
	regexp.MustCompile(`(?i)\bdeleted[ _-]?scene\b`),
	regexp.MustCompile(`(?i)\brecap\b`),
	regexp.MustCompile(`(?i)junk`),
}

// Is_junk reports whether the given file name matches a junk pattern.
func Is_junk(name string) bool {
	for _, re := range junk_patterns {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}

var (
	season_episode_re = regexp.MustCompile(`(?i)\bs(\d{1,2})e(\d{1,3})\b`)
	x_season_re       = regexp.MustCompile(`(?i)(\d{1,3})x(\d{1,3})\b`)
	season_word_re    = regexp.MustCompile(`(?i)\bseason\s+(\d{1,2})\s+episode\s+(\d{1,3})\b`)
	special_word_re   = regexp.MustCompile(`(?i)\bspecial\b`)
)

// Parse_episode_series extracts a series/season/episode hint from a file name.
// Matches "Show.S01E02", "Show.1x02", "Show.season 1 episode 2", and marks
// season 0 / "specials" as special content. Returns false for movie names.
// The "NxM" pattern can never swallow resolutions such as "1920x1080", since
// its width part is limited to three digits and must be followed by "x".
func Parse_episode_series(name string) (*Episode_hint, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))

	var match []int
	var season, episode int
	for _, re := range []*regexp.Regexp{season_episode_re, x_season_re, season_word_re} {
		m := re.FindStringSubmatchIndex(base)
		if m == nil {
			continue
		}
		match = m
		season, _ = strconv.Atoi(base[m[2]:m[3]])
		episode, _ = strconv.Atoi(base[m[4]:m[5]])
		break
	}
	if match == nil {
		return nil, false
	}

	hint := &Episode_hint{Season: season, Episode: episode, Is_special: season == 0}
	show := strings.TrimSpace(base[0:match[0]])
	show = strings.Trim(show, " _.-[]{}()")
	show = clean_separators(show)
	hint.Show_name = show

	if special_word_re.MatchString(base) && season != 0 {
		hint.Is_special = true
	}
	return hint, true
}

// clean_separators converts dot/underscore style separators into spaces while
// preserving legible words.
func clean_separators(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, ".", " ")
	return strings.Join(strings.Fields(s), " ")
}

// Classify maps a file name (and, optionally, its size) to a Classified
// verdict. size_bytes <= 0 skips the size check.
func Classify(name string, size_bytes int64, min_size_bytes int64) Classified {
	if !Is_video_file(name) {
		return Classified{Kind: Not_video}
	}
	if Is_junk(name) {
		return Classified{Kind: Junk}
	}
	if size_bytes > 0 && min_size_bytes > 0 && size_bytes < min_size_bytes {
		return Classified{Kind: Junk}
	}
	if hint, ok := Parse_episode_series(name); ok {
		return Classified{Kind: Video, Type: Series, Episode: hint}
	}
	return Classified{Kind: Video, Type: Movie}
}
