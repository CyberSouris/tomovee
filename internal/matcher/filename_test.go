package matcher

import "testing"

func Test_parse_filename_movies(t *testing.T) {
	cases := []struct {
		name  string
		title string
		year  int
	}{
		{"The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv", "The Matrix", 1999},
		{"Blade Runner 2049 (2017) [1080p] [BluRay].mkv", "Blade Runner 2049", 2017},
		{"2001.A.Space.Odyssey.1968.2160p.mkv", "2001 A Space Odyssey", 1968},
		{"1917.2019.1080p.WEB-DL.mkv", "1917", 2019},
		{"Interstellar (2014).mkv", "Interstellar", 2014},
		{"Spider-Man.2002.mkv", "Spider-Man", 2002},
		{"Some.Movie.1080p.mkv", "Some Movie", 0},
		{"2001.A.Space.Odyssey.mkv", "2001 A Space Odyssey", 0},
		{"No.Year.Movie.mkv", "No Year Movie", 0},
		{"Amelie.2001.720p.mkv", "Amelie", 2001},
		{"Dont.Breathe.2016.1080p.BluRay.x264.mkv", "Dont Breathe", 2016},
		{"The.Humans.1080p.BluRay.FGT.mkv", "The Humans", 0},
		{"UP.2009.1080p.BluRay.x264.mkv", "UP", 2009},
	}
	for _, c := range cases {
		got := Parse_filename(c.name)
		if got.Is_series {
			t.Errorf("Parse_filename(%q).Is_series = true, want false", c.name)
		}
		if got.Title != c.title {
			t.Errorf("Parse_filename(%q).Title = %q, want %q", c.name, got.Title, c.title)
		}
		if got.Year != c.year {
			t.Errorf("Parse_filename(%q).Year = %d, want %d", c.name, got.Year, c.year)
		}
	}
}

func Test_parse_filename_series(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		season  int
		episode int
	}{
		{"The Office S05E14 1080p.mkv", "The Office", 5, 14},
		{"Show.Name.S01E02.720p.WEB.mkv", "Show Name", 1, 2},
		{"Show - season 2 episode 3.mkv", "Show", 2, 3},
		{"Breaking.Bad.1x07.mkv", "Breaking Bad", 1, 7},
		{"Show.S00E01.Special.mkv", "Show", 0, 1},
	}
	for _, c := range cases {
		got := Parse_filename(c.name)
		if !got.Is_series {
			t.Errorf("Parse_filename(%q).Is_series = false, want true", c.name)
			continue
		}
		if got.Title != c.title {
			t.Errorf("Parse_filename(%q).Title = %q, want %q", c.name, got.Title, c.title)
		}
		if got.Season != c.season || got.Episode != c.episode {
			t.Errorf("Parse_filename(%q) = S%dE%d, want S%dE%d", c.name, got.Season, got.Episode, c.season, c.episode)
		}
	}
}

func Test_clean_title(t *testing.T) {
	cases := map[string]string{
		"The..Matrix__1999":       "The Matrix 1999",
		"Movie [1080p] (BluRay)":  "Movie",
		"Movie.1080p.BluRay.x264": "Movie",
		"Title.-RELEASEGROUP":     "Title",
		"  Trim.  Me.  ":          "Trim Me",
	}
	for in, want := range cases {
		if got := clean_title(in); got != want {
			t.Errorf("clean_title(%q) = %q, want %q", in, got, want)
		}
	}
}

func Test_parse_foldername(t *testing.T) {
	cases := []struct {
		name  string
		title string
		year  int
	}{
		{"The.Office", "The Office", 0},
		{"Breaking.Bad", "Breaking Bad", 0},
		{"The.Matrix.1999", "The Matrix", 1999},
		{"Chernobyl.2019", "Chernobyl", 2019},
		{"Stranger Things", "Stranger Things", 0},
		{"Dont Breathe", "Dont Breathe", 0},
	}
	for _, c := range cases {
		got := Parse_foldername(c.name)
		if got.Title != c.title || got.Year != c.year {
			t.Errorf("Parse_foldername(%q) = %q (%d), want %q (%d)", c.name, got.Title, got.Year, c.title, c.year)
		}
	}
}
