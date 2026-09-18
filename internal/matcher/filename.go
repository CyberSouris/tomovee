// Package matcher orchestrates identifying media files against external
// sources (OpenSubtitles hash, TMDB) and produces catalog metadata.
package matcher

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cybersouris/tomovee/internal/scanner"
)

// Filename_hint is the best guess at title/year/season/episode parsed from a
// file name, used to drive external searches.
type Filename_hint struct {
	Title     string
	Year      int
	Is_series bool
	Season    int
	Episode   int
}

var (
	year_re    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	bracket_re = regexp.MustCompile(`[\[\(\{][^\]\)\}]*[\]\)\}]`)
)

// release_tags are scene/quality tokens that carry no title information and
// are stripped when guessing a title. The list is deliberately conservative to
// avoid deleting ordinary title words.
var release_tags = map[string]bool{
	// resolution / quality
	"480p": true, "576p": true, "720p": true, "1080p": true, "2160p": true,
	"4k": true, "8k": true, "uhd": true, "fhd": true,
	// source
	"bluray": true, "blu-ray": true, "bdrip": true, "brrip": true, "bdremux": true,
	"webrip": true, "web-dl": true, "webdl": true, "hdtv": true,
	"dvdrip": true, "hdrip": true, "remux": true, "r5": true,
	// video codec
	"x264": true, "x265": true, "h264": true, "h265": true, "hevc": true,
	"avc": true, "mpeg2": true, "xvid": true, "av1": true, "vp9": true,
	// audio codec / channels
	"aac": true, "ac3": true, "eac3": true, "dts": true, "dts-hd": true,
	"truehd": true, "atmos": true, "flac": true, "opus": true,
	"dd5": true, "dd7": true,
	// other scene markers
	"proper": true, "repack": true, "unrated": true, "remastered": true,
	"subs": true, "subbed": true, "dubbed": true, "hdr": true,
	"hdr10": true, "dv": true, "10bit": true, "sdr": true,
}

// Parse_filename extracts a title/year (and series info when present) from a
// media file name. It is deliberately heuristic: it strips scene release tags,
// bracketed groups, and separator noise.
func Parse_filename(name string) Filename_hint {
	base := strings.TrimSuffix(name, filepath.Ext(name))

	if episode, ok := scanner.Parse_episode_series(name); ok {
		hint := Filename_hint{
			Title:     clean_title(episode.Show_name),
			Is_series: true,
			Season:    episode.Season,
			Episode:   episode.Episode,
		}
		if year, loc := last_year(base); year > 0 && loc != nil {
			hint.Year = year
		}
		return hint
	}

	year, loc := last_year(base)
	cutoff := len(base)
	if loc != nil && loc[0] > 0 {
		cutoff = loc[0]
	} else {
		year = 0
	}
	title := clean_title(base[:cutoff])
	if title == "" {
		title = clean_title(base)
	}
	return Filename_hint{Title: title, Year: year}
}

// last_year returns the last plausible release year in s and the location of
// its match, or 0/nil when none is found.
func last_year(s string) (int, []int) {
	matches := year_re.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return 0, nil
	}
	last := matches[len(matches)-1]
	year, err := strconv.Atoi(s[last[2]:last[3]])
	if err != nil {
		return 0, nil
	}
	return year, last
}

// clean_title removes bracketed groups, separator noise, leading-dash release
// tags, known scene tags, apostrophes, and trailing junk tokens from a raw
// title fragment.
func clean_title(raw string) string {
	raw = bracket_re.ReplaceAllString(raw, " ")
	raw = strings.NewReplacer(
		".", " ", "_", " ",
		"'", "", // "Dont Breathe" must match "Don't Breathe"
		"(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ",
	).Replace(raw)

	fields := strings.Fields(raw)
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if strings.HasPrefix(field, "-") {
			continue // trailing release group such as "…-RARBG"
		}
		if release_tags[strings.ToLower(field)] {
			continue
		}
		kept = append(kept, field)
	}
	// Drop trailing junk that is not part of a title, e.g. a bare release-group
	// acronym ("… FGT") or leftover quality token, unless a single token is the
	// whole title (the Pixar movie "UP").
	for len(kept) > 1 && is_junk_suffix(kept[len(kept)-1]) {
		kept = kept[:len(kept)-1]
	}
	out := strings.Trim(strings.Join(kept, " "), " -")
	return strings.Join(strings.Fields(out), " ")
}

// is_junk_suffix reports whether a trailing token looks like a release-group
// acronym or a leftover quality/container marker rather than a title word.
func is_junk_suffix(token string) bool {
	if len(token) < 2 || len(token) > 5 {
		return false
	}
	for _, r := range token {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
