package matcher

import "testing"

func Test_normalize_title(t *testing.T) {
	cases := map[string]string{
		"The Matrix":            "the matrix",
		"The.Matrix":            "the matrix",
		"The  Matrix (1999)":    "the matrix 1999",
		"Blade_Runner-2049":     "blade runner 2049",
		"  Trim  Spaces  ":      "trim spaces",
		"À Bientôt":             "à bientôt",
		"PI 3.14":               "pi 3 14",
		"Star Wars: A New Hope": "star wars a new hope",
	}
	for in, want := range cases {
		if got := normalize_title(in); got != want {
			t.Errorf("normalize_title(%q) = %q, want %q", in, got, want)
		}
	}
}

func Test_similarity(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"", "", 0},
		{"The Matrix", "", 0},
		{"The Matrix", "The Matrix", 1},
		{"The Matrix", "the-matrix", 1},
		{"Interstellar", "Interstellar 2014", 0},
		{"The Matrix", "The Matrix Reloaded", 0},
		{"Breaking Bad", "Breaking Bad", 1},
	}
	for _, c := range cases {
		got := similarity(c.a, c.b)
		switch c.want {
		case 1:
			if got != 1 {
				t.Errorf("similarity(%q, %q) = %v, want 1", c.a, c.b, got)
			}
		case 0:
			if got == 1 {
				t.Errorf("similarity(%q, %q) = 1, want < 1", c.a, c.b)
			}
		}
	}
}

func Test_similarity_partial(t *testing.T) {
	full := normalize_title("The Lord of the Rings: The Fellowship of the Ring")
	got := similarity("The Lord of the Rings", full)
	if got < 0.3 || got >= 1 {
		t.Errorf("partial similarity = %v, want middle-ish range", got)
	}
	exact := similarity("The Lord of the Rings: The Fellowship of the Ring", full)
	if exact != 1 {
		t.Errorf("exact similarity = %v, want 1", exact)
	}
	if got >= exact {
		t.Errorf("partial similarity %v should be below exact %v", got, exact)
	}
}

func Test_levenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"kitten", "", 6},
		{"", "sitting", 7},
		{"kitten", "kitten", 0},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"foo", "foobar", 3},
	}
	for _, c := range cases {
		if got := levenshtein([]rune(c.a), []rune(c.b)); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func Test_levenshtein_unicode(t *testing.T) {
	if got := levenshtein([]rune("café"), []rune("cafe")); got != 1 {
		t.Errorf("levenshtein(café, cafe) = %d, want 1", got)
	}
}

func Test_score_match(t *testing.T) {
	cases := []struct {
		query          string
		query_year     int
		candidate      string
		candidate_year int
		min            float64
		max            float64
	}{
		{"The Matrix", 1999, "The Matrix", 1999, 1, 1},              // identical + year bonus, clamped
		{"The Matrix", 0, "The Matrix", 1999, 0.85, 1},              // no year known, no bonus
		{"The Matrix", 1998, "The Matrix", 1999, 0.9, 1},            // one year off → +0.05
		{"The Matrix", 1990, "The Matrix", 1999, 0.7, 1},            // year mismatch → -0.25
		{"Interstellar", 2014, "Interstellar", 2014, 1, 1},          // match
		{"A Totally Different Film", 0, "The Matrix", 1999, 0, 0.5}, // weak title
	}
	for _, c := range cases {
		got := score_match(c.query, c.query_year, c.candidate, c.candidate_year)
		if got < c.min || got > c.max {
			t.Errorf("score_match(%q/%d, %q/%d) = %v, want in [%v, %v]",
				c.query, c.query_year, c.candidate, c.candidate_year, got, c.min, c.max)
		}
	}
}

func Test_score_match_clamped(t *testing.T) {
	if got := score_match("Matrix", 1990, "Matrix", 1999); got < 0 || got > 1 {
		t.Errorf("score out of range: %v", got)
	}
}
