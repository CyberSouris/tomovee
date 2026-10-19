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
	valid_files := []string{
		"Fight.Club.1999.1080p.BluRay.x264.mkv",
		"The Matrix (1999).mkv",
		"Marcel.Review.mkv",
		"Weekend.Movie.mkv",
	}
	for _, name := range valid_files {
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
		{"Show.Name_S01E02_Pilot.mkv", true, "Show Name", 1, 2, false},
		{"Show_Name_S01E02.mkv", true, "Show Name", 1, 2, false},
		{"Show_Name.1x02_Pilot.mkv", true, "Show Name", 1, 2, false},
		{"breaking bad s04e10.mkv", true, "breaking bad", 4, 10, false},
		{"Show.1x02.webrip.mkv", true, "Show", 1, 2, false},
		{"Series - season 3 episode 7.mkv", true, "Series", 3, 7, false},
		{"Show.S00E05.Pilot.mkv", true, "Show", 0, 5, true},
		{"Show.S01E02.Special.mkv", true, "Show", 1, 2, true},
		{"Show #01_02_The Female Factor.mp4", true, "Show", 1, 2, false},
		{"#01_02_The Female Factor.mp4", true, "", 1, 2, false},
		{"084 Bazén.mp4", true, "", 1, 84, false},
		{"001 - Pilot.mkv", true, "", 1, 1, false},
		{"Thundercats.S01.E16.mp4", true, "Thundercats", 1, 16, false},
		{"Movie.2020.1080p.mkv", false, "", 0, 0, false},
		{"Some.Thing.1920x1080.mkv", false, "", 0, 0, false},
		{"Some.Thing.1280x720.mkv", false, "", 0, 0, false},
		{"Another.One.4K.x264.mkv", false, "", 0, 0, false},
		{"2001 A Space Odyssey.mkv", false, "", 0, 0, false},
		{"101 Dalmatians (1996).mkv", false, "", 0, 0, false},
		{"0.5 Something.mkv", false, "", 0, 0, false},
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
	const min_size = 50 * 1024 * 1024

	cases := []struct {
		name  string
		size  int64
		kind  Media_kind
		media Media_type
	}{
		{"Movie.2020.mkv", 1 << 30, Video, Movie},
		{"Show.S01E02.mkv", 1 << 30, Video, Series},
		{"Show_Name_S01E02.mkv", 1 << 30, Video, Series},
		{"#01_02_The First Case.mp4", 1 << 30, Video, Series},
		{"084 Bazén.mp4", 1 << 30, Video, Series},
		{"Thundercats.S01.E16.mp4", 1 << 30, Video, Series},
		{"Samples/sample.mkv", 1 << 30, Junk, Movie},
		{"tiny.mkv", 1024, Junk, Movie},
		{"readme.txt", 1024, Not_video, Movie},
	}
	for _, c := range cases {
		got := Classify(c.name, c.size, min_size)
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
		{"season tagged with year", "Show.Name/S01 (2001)/S01E01.mkv", "Show.Name", "Show.Name"},
		{"season tagged with bracket year", "Show.Name/Season 2 [1999]/f.mkv", "Show.Name", "Show.Name"},
		{"two levels", "Show.Name/Season 1/Special Features/x.mkv", "Show.Name", "Show.Name"},
		{"prefixed season", "Show.Name/Show.Name Season 2/S02E01.mkv", "Show.Name", "Show.Name"},
		{"prefixed short season", "Show.Name/Show.Name.S03/S03E01.mkv", "Show.Name", "Show.Name"},
		{"prefixed season with episode folder", "Show.Name/Show.Name Season 1/Show.Name 1x02 - Pilot/Show.Name 1x02 - Pilot.mkv", "Show.Name", "Show.Name"},
		{"episode folder under show", "Show.Name/Show.Name 1x02 - Pilot/episode.mkv", "Show.Name", "Show.Name"},
		{"collection folder with absolute episodes", "Meadow Friends/Meadow Friends At The Cottage/084 The Pond.mp4", "Meadow Friends/Meadow Friends At The Cottage", "Meadow Friends At The Cottage"},
		{"series season folder", "Thundercats [1985-1989]/Thundercats.Series1/Thundercats.S01.E16.mp4", "Thundercats [1985-1989]", "Thundercats [1985-1989]"},
		{"numbered episode folder", "Show Name/47 Závan minulosti/episode.avi", "Show Name", "Show Name"},
		{"movie folder with year", "101 Dalmatians (1996)/101.Dalmatians.1996.mkv", "101 Dalmatians (1996)", "101 Dalmatians (1996)"},
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

	for _, folder := range []string{"Season 1", "Season 12", "s3", "S4", "S01 (2001)", "Season 2 [1999]", "Show.Name Season 2", "Show Name S03", "Show.Name.Season.4", "Series 1", "Series.12", "Thundercats.Series1", "Specials", "Specials", "Extras", "Bonus", "Special Features", "Deleted Scenes", "Behind the Scenes", "Trailers", "Interviews"} {
		if !Skip_series_folder(folder) {
			t.Errorf("Skip_series_folder(%q) = false, want true", folder)
		}
	}
	for _, folder := range []string{"Breaking Bad", "The.Office", "Santorini", "Friends 24", "Extras The Show", "The Office (2001)"} {
		if Skip_series_folder(folder) {
			t.Errorf("Skip_series_folder(%q) = true, want false", folder)
		}
	}
}

func Test_episode_hint_of_folder(t *testing.T) {
	const root = "/library"
	cases := []struct {
		name     string
		path     string
		want_ok  bool
		want_num int
	}{
		{"numbered folder", "/library/Show Name/47 Závan minulosti/episode.avi", true, 47},
		{"numbered folder uppercase", "/library/Show/084 Bazén/extra.mkv", true, 84},
		{"episode folder with marker", "/library/Show/Show.S02E03/file.mkv", true, 3},
		{"file in library root", "/library/episode.avi", false, 0},
		{"movie folder with year", "/library/Movies/101 Dalmatians (1996)/101.Dalmatians.1996.mkv", false, 0},
		{"numbered movie file", "/library/Movies/8 Mile/8.Mile.2002.mkv", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hint, ok := episode_hint_of_folder(c.path, root)
			if ok != c.want_ok {
				t.Fatalf("episode_hint_of_folder(%q) ok = %v, want %v", c.path, ok, c.want_ok)
			}
			if ok && hint.Episode != c.want_num {
				t.Errorf("episode_hint_of_folder(%q) episode = %d, want %d", c.path, hint.Episode, c.want_num)
			}
		})
	}
}

func Test_series_folder_patterns(t *testing.T) {
	cases := map[string]bool{
		"Season 1": true, "season 2": true, "Season.3": true, "S07": true, "s8": true,
		"S01 (2001)": true, "Season 2 [1999]": true,
		"Show.Name Season 2": true, "Show Name S03": true, "Show.Name.Season.4": true,
		"Series 1": true, "Thundercats.Series1": true,
		"Specials": true, "Special": true, "Extras": true, "Features": false,
		"Breaking.Bad": false, "The Office (2001)": false, "Friends 24": false,
	}
	for folder, want := range cases {
		if got := Skip_series_folder(folder); got != want {
			t.Errorf("Skip_series_folder(%q) = %v, want %v", folder, got, want)
		}
	}
}

func Test_season_of(t *testing.T) {
	cases := []struct {
		path        string
		want_season int
		want_ok     bool
	}{
		{"The.Office/Season 2/S01E01.mkv", 2, true},
		{"The.Office/Season.10/Part 1.mkv", 10, true},
		{"The.Office/s3/Part 1.mkv", 3, true},
		{"The.Office/S04/Part 1.mkv", 4, true},
		{"The.Office/S02 (2001)/Part 1.mkv", 2, true},
		{"The.Office/Season 3 [2005]/Part 1.mkv", 3, true},
		{"The.Office/The.Office Season 2/S01E01.mkv", 2, true},
		{"The.Office/The.Office.S10/Part 1.mkv", 10, true},
		{"The.Office/The.Office 1x02 - Pilot/Part 1.mkv", 0, false},
		{"The.Office/Specials/Bloopers.mkv", 0, true},
		{"The.Office/Extras/Behind the Scenes.mkv", 0, true},
		{"The.Office/Part 1.mkv", 0, false},
		{"S01E01.mkv", 0, false},
		{"The.Office/Season One/Part 1.mkv", 0, false},
	}
	for _, tc := range cases {
		season, ok := Season_of(tc.path)
		if season != tc.want_season || ok != tc.want_ok {
			t.Errorf("Season_of(%q) = %d, %v, want %d, %v", tc.path, season, ok, tc.want_season, tc.want_ok)
		}
	}
}
