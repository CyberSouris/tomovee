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
