package scanner

import "testing"

func Test_is_video_file(t *testing.T) {
	cases := map[string]bool{
		"movie.mkv":  true,
		"movie.MKV":  true,
		"movie.mp4":  true,
		"movie.avi":  true,
		"movie.m2ts": true,
		"movie.jpeg": false,
		"movie.txt":  false,
		"movie":      false,
	}
	for name, want := range cases {
		if got := Is_video_file(name); got != want {
			t.Errorf("Is_video_file(%q) = %v, want %v", name, got, want)
		}
	}
}

func Test_is_junk(t *testing.T) {
	junk := []string{
		"Fight.Club.1999.Sample.mkv",
		"trailer.mkv",
		"Something.Et-trailer.mkv",
		"Extra features/extras.mkv",
		"The.Thing.1982.Featurette.mkv",
		"Behind the Scenes.mkv",
		"blooper reel.mkv",
		"Movie.Intro.mkv",
		"Generic.Junk.mkv",
		"credits.mkv",
		"teaser.mkv",
	}
	for _, name := range junk {
		if !Is_junk(name) {
			t.Errorf("Is_junk(%q) = false, want true", name)
		}
	}
	real := []string{
		"Fight.Club.1999.1080p.BluRay.x264.mkv",
		"The Matrix (1999).mkv",
		"Marcel.Review.mkv",
		"Weekend.Movie.mkv",
	}
	for _, name := range real {
		if Is_junk(name) {
			t.Errorf("Is_junk(%q) = true, want false", name)
		}
	}
}

func Test_parse_episode_series(t *testing.T) {
	cases := []struct {
		name         string
		want_ok      bool
		want_show    string
		want_season  int
		want_episode int
		want_special bool
	}{
		{"The.Office.S01E02.720p.mkv", true, "The Office", 1, 2, false},
		{"breaking bad s04e10.mkv", true, "breaking bad", 4, 10, false},
		{"Show.1x02.webrip.mkv", true, "Show", 1, 2, false},
		{"Series - season 3 episode 7.mkv", true, "Series", 3, 7, false},
		{"Show.S00E05.Pilot.mkv", true, "Show", 0, 5, true},
		{"Show.S01E02.Special.mkv", true, "Show", 1, 2, true},
		{"Movie.2020.1080p.mkv", false, "", 0, 0, false},
		{"Some.Thing.1920x1080.mkv", false, "", 0, 0, false},
		{"Another.One.4K.x264.mkv", false, "", 0, 0, false},
	}
	for _, c := range cases {
		hint, ok := Parse_episode_series(c.name)
		if ok != c.want_ok {
			t.Errorf("Parse_episode_series(%q) ok = %v, want %v", c.name, ok, c.want_ok)
			continue
		}
		if !ok {
			continue
		}
		if hint.Show_name != c.want_show {
			t.Errorf("Parse_episode_series(%q) show = %q, want %q", c.name, hint.Show_name, c.want_show)
		}
		if hint.Season != c.want_season || hint.Episode != c.want_episode {
			t.Errorf("Parse_episode_series(%q) = S%dE%d, want S%dE%d", c.name, hint.Season, hint.Episode, c.want_season, c.want_episode)
		}
		if hint.Is_special != c.want_special {
			t.Errorf("Parse_episode_series(%q) special = %v, want %v", c.name, hint.Is_special, c.want_special)
		}
	}
}

func Test_classify(t *testing.T) {
	const min = 50 * 1024 * 1024

	cases := []struct {
		name  string
		size  int64
		kind  Media_kind
		media Media_type
	}{
		{"Movie.2020.mkv", 1 << 30, Video, Movie},
		{"Show.S01E02.mkv", 1 << 30, Video, Series},
		{"Samples/sample.mkv", 1 << 30, Junk, Movie},
		{"tiny.mkv", 1024, Junk, Movie},
		{"readme.txt", 1024, Not_video, Movie},
	}
	for _, c := range cases {
		got := Classify(c.name, c.size, min)
		if got.Kind != c.kind {
			t.Errorf("Classify(%q).Kind = %v, want %v", c.name, got.Kind, c.kind)
		}
		if got.Kind == Video && got.Type != c.media {
			t.Errorf("Classify(%q).Type = %v, want %v", c.name, got.Type, c.media)
		}
	}
}

func Test_series_folder_helpers(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		want_root string
		want_name string
	}{
		{"nested season", "Show.Name/Season 1/S01E01.mkv", "Show.Name", "Show.Name"},
		{"flat show", "Show.Name/S01E01.mkv", "Show.Name", "Show.Name"},
		{"nested specials", "Show.Name/Specials/Show.S00E01.mkv", "Show.Name", "Show.Name"},
		{"season s1", "Show.Name/S1/f.mkv", "Show.Name", "Show.Name"},
		{"two levels", "Show.Name/Season 1/Special Features/x.mkv", "Show.Name", "Show.Name"},
		{"flat in library root", "S01E01.mkv", ".", ""},
		{"flat season in library root", "Season 1/S01E01.mkv", ".", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Series_root_of(tc.path); got != tc.want_root {
				t.Errorf("Series_root_of(%q) = %q, want %q", tc.path, got, tc.want_root)
			}
			if got := Series_folder_name(tc.path); got != tc.want_name {
				t.Errorf("Series_folder_name(%q) = %q, want %q", tc.path, got, tc.want_name)
			}
		})
	}

	for _, folder := range []string{"Season 1", "Season 12", "s3", "S4", "Specials", "Specials", "Extras", "Bonus", "Special Features", "Deleted Scenes", "Behind the Scenes", "Trailers", "Interviews"} {
		if !Skip_series_folder(folder) {
			t.Errorf("Skip_series_folder(%q) = false, want true", folder)
		}
	}
	for _, folder := range []string{"Breaking Bad", "The.Office", "Santorini", "Extras The Show"} {
		if Skip_series_folder(folder) {
			t.Errorf("Skip_series_folder(%q) = true, want false", folder)
		}
	}
}

func Test_series_folder_patterns(t *testing.T) {
	cases := map[string]bool{
		"Season 1": true, "season 2": true, "Season.3": true, "S07": true, "s8": true,
		"Specials": true, "Special": true, "Extras": true, "Features": false,
		"Breaking.Bad": false,
	}
	for folder, want := range cases {
		if got := Skip_series_folder(folder); got != want {
			t.Errorf("Skip_series_folder(%q) = %v, want %v", folder, got, want)
		}
	}
}
