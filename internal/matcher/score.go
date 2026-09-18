package matcher

import (
	"strings"
	"unicode"
)

// normalize_title lower-cases and reduces a title to space-separated
// alphanumeric tokens, so punctuation, case, and apostrophes do not affect
// comparison. Apostrophes are dropped rather than spaced so a file name that
// omits them ("Dont Breathe") aligns with the official title ("Don't Breathe").
func normalize_title(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if r != '\'' {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// similarity returns a 0..1 ratio between two titles based on Levenshtein
// distance over their normalized forms, boosted when one title is a clean
// suffix of the other.
func similarity(a, b string) float64 {
	an, bn := normalize_title(a), normalize_title(b)
	if an == "" || bn == "" {
		return 0
	}
	if an == bn {
		return 1
	}
	ar, br := []rune(an), []rune(bn)
	dist := levenshtein(ar, br)
	ratio := 1 - float64(dist)/float64(max(len(ar), len(br)))
	if ratio < 0 {
		ratio = 0
	}
	if suffix_submatch(a, b) {
		ratio = max(ratio, 0.85)
	}
	return ratio
}

// suffix_submatch reports whether one normalized title is a contiguous suffix
// of the other and makes up at least half of it. A file name frequently drops
// a leading article or credit ("Shawshank Redemption" for "The Shawshank
// Redemption", "Psycho" for "Alfred Hitchcock's Psycho"); boosting that case
// keeps the match confident. Sequels and spin-offs, whose extra words trail
// the title ("The Matrix Reloaded"), are not a suffix and stay un-boosted.
func suffix_submatch(a, b string) bool {
	ta, tb := strings.Fields(normalize_title(a)), strings.Fields(normalize_title(b))
	return suffix_tokens(ta, tb) || suffix_tokens(tb, ta)
}

func suffix_tokens(short, long []string) bool {
	if len(short) == 0 || len(short) >= len(long) {
		return false
	}
	if count_chars(short)*2 < count_chars(long) {
		return false
	}
	offset := len(long) - len(short)
	for i, token := range short {
		if token != long[offset+i] {
			return false
		}
	}
	return true
}

func count_chars(tokens []string) int {
	n := 0
	for _, token := range tokens {
		n += len(token)
	}
	return n
}

// levenshtein computes the edit distance between two rune slices.
func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// score_match combines title similarity with a release-year agreement bonus or
// penalty, clamped to 0..1.
func score_match(query string, year int, candidate_title string, candidate_year int) float64 {
	score := similarity(query, candidate_title)
	if year > 0 && candidate_year > 0 {
		switch d := abs(year - candidate_year); {
		case d == 0:
			score += 0.15
		case d <= 1:
			score += 0.05
		default:
			score -= 0.25
		}
	}
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
