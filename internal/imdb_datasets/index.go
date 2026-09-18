package imdb_datasets

import (
	"strings"
	"unicode"

	"github.com/cybersouris/tomovee/internal/scanner"
)

// Result is one offline match produced by Index.Search.
type Result struct {
	Id              string
	Title           string
	Original_title  string
	Year            int
	End_year        int
	Media_type      scanner.Media_type
	Runtime_minutes int
	Genres          []string
	Rating          float64
	Votes           int
}

// Episode_ref links an episode's tconst to its position within a series.
type Episode_ref struct {
	Id         string
	Season     int
	Episode    int
	Is_special bool
}

// Index holds the parsed IMDb datasets in memory: exact-match lookups on
// normalized primary, original, and alternative (akas) titles, an episode
// index, and ratings.
type Index struct {
	titles   []Title
	by_key   map[string][]*Title
	by_id    map[string]*Title
	aka_key  map[string][]string
	episodes map[string][]Episode_ref
	ratings  map[string]Rating
	count    int // non-catalogued (e.g. short/video) rows seen
}

// New_index builds an Index from parsed title.basics records.
func New_index(titles []Title) *Index {
	index := &Index{
		by_key:   make(map[string][]*Title),
		by_id:    make(map[string]*Title),
		aka_key:  make(map[string][]string),
		episodes: make(map[string][]Episode_ref),
		ratings:  make(map[string]Rating),
	}
	index.titles = titles
	for i := range titles {
		title := &titles[i]
		if title.Id != "" {
			index.by_id[title.Id] = title
		}
		if _, ok := media_type_of(title.Title_type); !ok {
			index.count++
			continue
		}
		if title.Primary_title != "" {
			key := Normalize_title(title.Primary_title)
			index.by_key[key] = append(index.by_key[key], title)
		}
		if title.Original_title != "" && title.Original_title != title.Primary_title {
			key := Normalize_title(title.Original_title)
			index.by_key[key] = append(index.by_key[key], title)
		}
	}
	return index
}

// Index_akas adds the alternative titles from title.akas to the search index.
// Rows that are just the original title are skipped; the deduplication in
// Search collapses any remaining overlaps.
func (idx *Index) Index_akas(akas []Akas) {
	for _, aka := range akas {
		if aka.Id == "" || aka.Is_original {
			continue
		}
		key := Normalize_title(aka.Title)
		if key == "" {
			continue
		}
		idx.aka_key[key] = append(idx.aka_key[key], aka.Id)
	}
}

// Index_episodes adds the parent → episode mapping from title.episode.
func (idx *Index) Index_episodes(episodes []Episode) {
	for _, ep := range episodes {
		if ep.Id == "" || ep.Parent_id == "" {
			continue
		}
		ref := Episode_ref{Id: ep.Id, Season: ep.Season, Episode: ep.Episode}
		if ep.Season == 0 {
			ref.Is_special = true
		}
		idx.episodes[ep.Parent_id] = append(idx.episodes[ep.Parent_id], ref)
	}
}

// Index_ratings adds the title.ratings data.
func (idx *Index) Index_ratings(ratings []Rating) {
	for _, rating := range ratings {
		if rating.Id == "" {
			continue
		}
		idx.ratings[rating.Id] = rating
	}
}

// Count returns how many catalogued titles the index holds.
func (idx *Index) Count() int {
	return len(idx.titles) - idx.count
}

// Lookup returns the title record for an IMDb id (tconst form), if present.
func (idx *Index) Lookup(id string) (*Title, bool) {
	title, ok := idx.by_id[id]
	return title, ok
}

// Rating returns the ratings record for an IMDb id, if present.
func (idx *Index) Rating(id string) (Rating, bool) {
	rating, ok := idx.ratings[id]
	return rating, ok
}

// Episodes returns the episode references of a series in broadcast order.
func (idx *Index) Episodes(parent string) []Episode_ref {
	episodes := idx.episodes[parent]
	out := make([]Episode_ref, 0, len(episodes))
	for i := range episodes {
		out = append(out, episodes[i])
	}
	return out
}

// Episode_lookup returns the episode reference for a series at the given
// season and episode numbers, if the dataset has it.
func (idx *Index) Episode_lookup(parent string, season, episode int) (Episode_ref, bool) {
	for _, ref := range idx.episodes[parent] {
		if ref.Season == season && ref.Episode == episode {
			return ref, true
		}
	}
	return Episode_ref{}, false
}

// Search returns the titles whose normalized primary, original, or alternative
// title equals the normalized query. Results are restricted to the requested
// media type when want is Movie or Series. A non-zero year filters nothing on
// its own; callers rank matches by year closeness.
func (idx *Index) Search(query string, year int, want scanner.Media_type) []Result {
	key := Normalize_title(query)
	if key == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []Result
	append_title := func(title *Title) {
		media_type, ok := media_type_of(title.Title_type)
		if !ok {
			return
		}
		if want == scanner.Movie && media_type != scanner.Movie {
			return
		}
		if want == scanner.Series && media_type != scanner.Series {
			return
		}
		if seen[title.Id] {
			return
		}
		seen[title.Id] = true
		result := Result{
			Id:              title.Id,
			Title:           title.Primary_title,
			Original_title:  title.Original_title,
			Year:            title.Start_year,
			End_year:        title.End_year,
			Media_type:      media_type,
			Runtime_minutes: title.Runtime_minutes,
			Genres:          split_genres(title.Genres),
		}
		if rating, ok := idx.ratings[title.Id]; ok {
			result.Rating = rating.Average_rating
			result.Votes = rating.Num_votes
		}
		out = append(out, result)
	}
	for _, title := range idx.by_key[key] {
		append_title(title)
	}
	for _, id := range idx.aka_key[key] {
		if title, ok := idx.by_id[id]; ok {
			append_title(title)
		}
	}
	return out
}

// Normalize_title lower-cases and reduces a title to space-separated
// alphanumeric tokens, matching how filenames are normalized during matching.
func Normalize_title(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func split_genres(raw string) []string {
	raw = clean_null(raw)
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}
