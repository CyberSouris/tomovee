package matcher

import (
	"strings"
	"unicode"
)

// normalize_title lower-cases and reduces a title to space-separated
// alphanumeric tokens, so punctuation and case do not affect comparison.
func normalize_title(s string) string {
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

// similarity returns a 0..1 ratio between two titles based on Levenshtein
// distance over their normalized forms.
func similarity(a, b string) float64 {
	a = normalize_title(a)
	b = normalize_title(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ar, br := []rune(a), []rune(b)
	dist := levenshtein(ar, br)
	max_len := max(len(ar), len(br))
	ratio := 1 - float64(dist)/float64(max_len)
	if ratio < 0 {
		return 0
	}
	return ratio
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
