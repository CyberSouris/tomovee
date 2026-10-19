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
	// season_episode_re matches the "S01E02" and "S01.E02" spellings: the season
	// and episode halves may be joined directly or by a separator.
	season_episode_re = regexp.MustCompile(`(?i)\bs(\d{1,2})[\s._-]*e(\d{1,3})\b`)
	x_season_re       = regexp.MustCompile(`(?i)\b(\d{1,3})x(\d{1,3})\b`)
	season_word_re    = regexp.MustCompile(`(?i)\bseason\s+(\d{1,2})\s+episode\s+(\d{1,3})\b`)
	// hash_season_re matches the "#SS_EE" spelling used by some trackers and
	// media managers, where the season and episode are joined by a separator
	// after a leading "#" ("#01_02_The Female Factor.mp4"). The "#" keeps the
	// two numbers from being read out of unrelated pairs such as "2019 1080p".
	hash_season_re  = regexp.MustCompile(`(?i)#(\d{1,2})[\s._-]+(\d{1,3})\b`)
	special_word_re = regexp.MustCompile(`(?i)\bspecial\b`)
	// overall_episode_re matches an episode named by an absolute, zero-padded
	// number at the start of the name, as a show's numbered collections are
	// ("084 Bazén.mp4" is episode 84). The leading zero separates it from a
	// release year or a numbered title ("2001 A Space Odyssey",
	// "101 Dalmatians"), which never start with "0"; a separator and some text
	// must follow so a bare "01" is left alone.
	overall_episode_re = regexp.MustCompile(`^\s*0(\d{1,3})[\s._-]+\S`)
	// numbered_folder_re matches a folder named after an episode by its bare
	// number, as collections that give every episode its own folder do
	// ("47 Závan minulosti"). Unlike a file name, a folder carries no release
	// year, so a leading number is a strong episode signal there.
	numbered_folder_re = regexp.MustCompile(`^\s*(\d{1,3})[\s._-]+\S`)
	// year_in_name_re matches a four-digit release year, which marks a name as a
	// movie rather than a numbered episode.
	year_in_name_re = regexp.MustCompile(`(?:19|20)\d{2}`)
)

// Parse_episode_series extracts a series/season/episode hint from a file name.
// Matches "Show.S01E02", "Show.1x02", "Show.season 1 episode 2", the "#01_02"
// tracker spelling, and collections numbered by an absolute episode count
// ("084 Bazén.mp4"), and marks season 0 / "specials" as special content.
// Returns false for movie names.
// The "NxM" pattern can never swallow resolutions such as "1920x1080" or
// "1280x720": it must start at a word boundary and its parts are limited to
// three digits.
func Parse_episode_series(name string) (*Episode_hint, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if hint, ok := parse_episode_base(base); ok {
		return hint, true
	}
	return parse_overall_episode(base)
}

// episode_patterns are tried in order; the first one matching wins.
var episode_patterns = []*regexp.Regexp{season_episode_re, x_season_re, season_word_re, hash_season_re}

// parse_episode_base extracts a season/episode hint from a name whose file
// extension, if any, has already been removed. It is shared with folder-name
// detection, which must not strip anything after a dot: a folder such as
// "Show.Name 1x02 - Pilot" keeps its episode marker even though it follows a dot.
func parse_episode_base(base string) (*Episode_hint, bool) {
	// Underscores are separator noise, not word characters, so the \b anchors
	// in the patterns below must not treat them as part of a word: without this
	// "Show_S01E01.mkv" and "Show.S01E01_Pilot.mkv" go unrecognized and an
	// episode is catalogued as a movie. "_" and " " are both one byte, so the
	// match offsets stay valid for slicing the show name later.
	base = strings.ReplaceAll(base, "_", " ")

	var match []int
	var season, episode int
	for _, re := range episode_patterns {
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

// overall_episode_season is the season an absolute-numbered episode is filed
// under. Those names carry no season of their own, and the parser only sees the
// file name rather than the show's container folder, so season 1 stands in for
// the show's first (or only) season.
const overall_episode_season = 1

// parse_overall_episode recognises an episode numbered by an absolute count
// ("084 Bazén.mp4") rather than by a season/episode marker: a show split into
// collections often numbers every file in one running sequence. Only the file
// name is considered, so the number becomes the episode of
// overall_episode_season; the show name is empty because the numbering sits at
// the very start of the name, and callers fall back to the containing folder
// for the title. It is deliberately kept out of parse_episode_base so a
// numbered folder is not mistaken for a per-episode container.
func parse_overall_episode(base string) (*Episode_hint, bool) {
	return parse_numbered_episode(base, overall_episode_re)
}

// parse_numbered_episode_folder recognises a folder named by a bare episode
// number ("47 Závan minulosti", "084 Bazén"): the number becomes the episode of
// overall_episode_season, the same default an absolute-numbered file gets. The
// leading zero that guards a file name is not required here, because a folder
// has no release year to be confused with; parse_episode_folder screens the
// movie-folder shape before calling this.
func parse_numbered_episode_folder(name string) (*Episode_hint, bool) {
	return parse_numbered_episode(name, numbered_folder_re)
}

// parse_numbered_episode extracts an absolute episode number using re, whose
// group 1 is the number. The number becomes the episode of
// overall_episode_season; a zero number is not an episode.
func parse_numbered_episode(name string, re *regexp.Regexp) (*Episode_hint, bool) {
	name = strings.ReplaceAll(name, "_", " ")
	match := re.FindStringSubmatchIndex(name)
	if match == nil {
		return nil, false
	}
	number, _ := strconv.Atoi(name[match[2]:match[3]])
	if number == 0 {
		return nil, false
	}
	return &Episode_hint{
		Season:     overall_episode_season,
		Episode:    number,
		Is_special: special_word_re.MatchString(name),
	}, true
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

// season_folder_re matches the container folders that name a season: "Season 2",
// "Season.2", the British "Series 2", and the short "S2" spelling, whether the
// folder is bare or is prefixed with the show name ("Show Name Season 2",
// "Show.Name.S02", "Thundercats.Series1"). Group 1 is the season number. The
// short spelling must start a token so that a show name merely ending in "s"
// ("Friends 2012") is not mistaken for one.
var season_folder_re = regexp.MustCompile(`(?i)(?:^|[\s._-])(?:season|series|s)\s*[\s._-]*(\d{1,3})$`)

// series_folder_patterns matches directory names that hold episodes for a show
// rather than naming the show itself: season folders, specials, extras, and
// similar containers. A series' identity is the folder above these.
var series_folder_patterns = []*regexp.Regexp{
	season_folder_re,
	regexp.MustCompile(`(?i)^specials?$`),
	regexp.MustCompile(`(?i)^extras?$`),
	regexp.MustCompile(`(?i)^bonus(\s*\w*)$`),
	regexp.MustCompile(`(?i)^(featurettes?|special\s+features?|deleted\s+scenes)$`),
	regexp.MustCompile(`(?i)^(behind\s+the\s+scenes|interviews?|trailers?)$`),
	regexp.MustCompile(`(?i)^season.*extra$`),
}

// folder_year_tag_re matches a trailing year tag in parentheses or brackets,
// with an optional separator, such as the " (2001)" in "S01 (2001)" or the
// " [1999]" in "Season 2 [1999]". Release layouts routinely append the season's
// year to the container folder; the tag says nothing about the show's identity,
// so it is dropped before deciding whether a folder names a season.
var folder_year_tag_re = regexp.MustCompile(`[\s._-]*[\(\[]\s*(?:19|20)\d{2}\s*[\)\]][\s._-]*$`)

// strip_folder_year_tag removes a trailing year tag from a folder name so that
// "S01 (2001)" is treated the same as "S01".
func strip_folder_year_tag(name string) string {
	return strings.Trim(folder_year_tag_re.ReplaceAllString(name, ""), " ._-")
}

// Skip_series_folder reports whether a directory base name is a container
// inside a show's folder tree (Season 1, S01 (2001), Specials, Extras, ...)
// rather than the show title itself.
func Skip_series_folder(name string) bool {
	cleaned := strings.Trim(name, " ._-")
	for _, candidate := range []string{cleaned, strip_folder_year_tag(cleaned)} {
		for _, re := range series_folder_patterns {
			if re.MatchString(candidate) {
				return true
			}
		}
	}
	return false
}

// Season_of returns the season the container folder of the file at path
// declares: "Season 3", "S3" and their year-tagged forms ("S03 (2001)") give 3,
// while specials and extras containers ("Specials", "Extras", "Behind the
// Scenes", ...) give 0, the season specials live in. ok is false when the file
// sits directly in the show folder, which names no season, so the caller keeps
// its own default.
func Season_of(path string) (season int, ok bool) {
	name := strings.Trim(filepath.Base(filepath.Dir(path)), " ._-")
	name = strip_folder_year_tag(name)
	if match := season_folder_re.FindStringSubmatch(name); match != nil {
		if number, err := strconv.Atoi(match[1]); err == nil {
			return number, true
		}
	}
	if Skip_series_folder(name) {
		return 0, true
	}
	return 0, false
}

// Series_root_of returns the directory that names the series for a file at
// path, stepping out of every container between the file and the show: season
// folders ("Season 2", "Show.Name.S02"), specials/extras folders, and folders
// named after the single episode they hold ("Show.S01E01.Pilot", "Show 1x02 -
// Daisy's Song"). When the immediate parent is a plain show folder that folder
// is returned unchanged; container folders are peeled until the first
// non-container one. File paths may be absolute or relative to a library root.
func Series_root_of(path string) string {
	dir := filepath.Dir(path)
	for is_series_container(filepath.Base(dir)) {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dir
}

// is_series_container reports whether a directory base name is a container
// inside a show's folder tree rather than the show title itself: a
// season/specials/extras folder, or a folder named after the episode it holds.
func is_series_container(name string) bool {
	if Skip_series_folder(name) {
		return true
	}
	_, ok := parse_episode_folder(name)
	return ok
}

// parse_episode_folder returns the episode a directory name declares, either by
// an explicit season/episode marker ("Show 1x02 - Pilot") or by the bare number
// some collections give each episode's own folder ("47 Závan minulosti"). A
// folder carrying a release year is a movie folder, so the bare number is only
// trusted when no year tag is present.
func parse_episode_folder(name string) (*Episode_hint, bool) {
	if hint, ok := parse_episode_base(name); ok {
		return hint, true
	}
	if folder_year_tag_re.MatchString(name) {
		return nil, false
	}
	return parse_numbered_episode_folder(name)
}

// episode_hint_of_folder returns the episode a file's containing folder names,
// for a file that has no episode marker of its own but sits in an episode
// folder ("47 Závan minulosti/želvy ninja 47.avi"). The library root is never
// treated as an episode folder, and a file carrying a release year is left as a
// movie.
func episode_hint_of_folder(path, root string) (*Episode_hint, bool) {
	dir := filepath.Dir(path)
	if dir == root {
		return nil, false
	}
	if year_in_name_re.MatchString(filepath.Base(path)) {
		return nil, false
	}
	return parse_episode_folder(filepath.Base(dir))
}

// Series_folder_name returns the base name of the directory that names the
// series for a file at path, or "" when the path has no usable series folder
// (episodes sat directly in the library root, next to nothing to step out
// into). Callers fall back to the file name when this is empty.
func Series_folder_name(path string) string {
	name := filepath.Base(Series_root_of(path))
	if name == "." || name == "" || name == string(filepath.Separator) || name == "/" {
		return ""
	}
	return name
}
