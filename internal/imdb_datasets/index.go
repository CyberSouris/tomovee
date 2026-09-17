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
}

// Index holds the parsed title.basics dataset in memory with an exact-match
// lookup on normalized primary and original titles.
type Index struct {
	titles []Title
	by_key map[string][]*Title
	by_id  map[string]*Title
	count  int // non-catalogued (e.g. short/video) rows seen
}

// New_index builds an Index from parsed titles.
func New_index(titles []Title) *Index {
	index := &Index{
		by_key: make(map[string][]*Title),
		by_id:  make(map[string]*Title),
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

// Count returns how many catalogued titles the index holds.
func (idx *Index) Count() int {
	return len(idx.titles) - idx.count
}

// Lookup returns the title record for an IMDb id (tconst form), if present.
func (idx *Index) Lookup(id string) (*Title, bool) {
	title, ok := idx.by_id[id]
	return title, ok
}

// Search returns the titles whose normalized primary or original title equals
// the normalized query. Results are restricted to the requested media type
// when want is Movie or Series. A non-zero year filters nothing on its own;
// callers rank matches by year closeness.
func (idx *Index) Search(query string, year int, want scanner.Media_type) []Result {
	key := Normalize_title(query)
	if key == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []Result
	for _, title := range idx.by_key[key] {
		media_type, ok := media_type_of(title.Title_type)
		if !ok {
			continue
		}
		if want == scanner.Movie && media_type != scanner.Movie {
			continue
		}
		if want == scanner.Series && media_type != scanner.Series {
			continue
		}
		if seen[title.Id] {
			continue
		}
		seen[title.Id] = true
		out = append(out, Result{
			Id:              title.Id,
			Title:           title.Primary_title,
			Original_title:  title.Original_title,
			Year:            title.Start_year,
			End_year:        title.End_year,
			Media_type:      media_type,
			Runtime_minutes: title.Runtime_minutes,
			Genres:          split_genres(title.Genres),
		})
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
