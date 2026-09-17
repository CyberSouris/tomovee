package metadata

import (
	"os"
	"reflect"
	"testing"
)

func load_fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func Test_parse_mp4_fixture(t *testing.T) {
	info, err := Parse_from_json("/movies/example.mp4", load_fixture(t, "sample.mp4.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Container != "mov,mp4,m4a,3gp,3g2,mj2" {
		t.Errorf("container = %q", info.Container)
	}
	if info.Duration_seconds != 3.0 {
		t.Errorf("duration = %v, want 3", info.Duration_seconds)
	}
	if info.Size_bytes != 89872 {
		t.Errorf("size = %d, want 89872", info.Size_bytes)
	}
	if info.Video == nil {
		t.Fatal("expected a video stream")
	}
	v := info.Video
	if v.Codec != "h264" || v.Width != 1280 || v.Height != 720 {
		t.Errorf("video = %+v", v)
	}
	if v.Resolution_label != "720p" {
		t.Errorf("resolution label = %q, want 720p", v.Resolution_label)
	}
	if v.Bit_depth != 8 {
		t.Errorf("bit depth = %d, want 8", v.Bit_depth)
	}
	if v.Frame_rate != 24.0 {
		t.Errorf("frame rate = %v, want 24", v.Frame_rate)
	}
	if v.Hdr {
		t.Error("expected no HDR on sdr fixture")
	}
	if len(info.Audio) != 2 {
		t.Fatalf("audio streams = %d, want 2", len(info.Audio))
	}
	if got := info.Audio[0]; got.Language != "eng" || got.Codec != "aac" || got.Channels != 1 {
		t.Errorf("audio[0] = %+v", got)
	}
	if got := info.Audio[1]; got.Language != "fra" {
		t.Errorf("audio[1] = %+v", got)
	}
	if len(info.Subtitles) != 0 {
		t.Errorf("subtitles = %+v, want none", info.Subtitles)
	}
}

func Test_parse_mkv_fixture(t *testing.T) {
	info, err := Parse_from_json("/shows/ep1.mkv", load_fixture(t, "rich.mkv.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Container != "matroska,webm" {
		t.Errorf("container = %q, want matroska,webm", info.Container)
	}
	if info.Video == nil {
		t.Fatal("expected a video stream")
	}
	if info.Video.Resolution_label != "1080p" {
		t.Errorf("resolution label = %q, want 1080p", info.Video.Resolution_label)
	}
	if info.Video.Frame_rate != 24.0 {
		t.Errorf("frame rate = %v, want 24", info.Video.Frame_rate)
	}
	if len(info.Audio) != 2 {
		t.Errorf("audio streams = %d, want 2", len(info.Audio))
	}
	if len(info.Subtitles) != 1 {
		t.Fatalf("subtitle streams = %d, want 1", len(info.Subtitles))
	}
	sub := info.Subtitles[0]
	if sub.Language != "eng" || sub.Format != "subrip" {
		t.Errorf("subtitle = %+v", sub)
	}
}

func Test_tolerates_unknown_stream_types(t *testing.T) {
	input := `{
		"streams": [
			{"index": 0, "codec_type": "data", "codec_name": "bin_data"},
			{"index": 1, "codec_type": "video", "codec_name": "h264",
			 "width": 1920, "height": 1080, "avg_frame_rate": "24/1",
			 "bits_per_raw_sample": "8", "field_order": "progressive"},
			{"index": 2, "codec_type": "attachment", "codec_name": "truetype"}
		],
		"format": {"format_name": "matroska,webm", "duration": "100.5", "bit_rate": "1000000"}
	}`
	info, err := Parse_from_json("/x.mkv", []byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(info.Warnings) != 2 {
		t.Errorf("warnings = %v, want 2 (data + attachment)", info.Warnings)
	}
	if info.Video == nil || info.Video.Width != 1920 {
		t.Error("video stream should still be parsed")
	}
	if info.Duration_seconds != 100.5 || info.Bit_rate != 1000000 {
		t.Errorf("format = %+v", info)
	}
}

func Test_parse_invalid_json_is_error(t *testing.T) {
	if _, err := Parse_from_json("/x.mp4", []byte("{not json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func Test_resolution_label(t *testing.T) {
	cases := []struct {
		width, height int
		interlaced    bool
		want          string
	}{
		{7680, 4320, false, "8Kp"},
		{3840, 2160, false, "4Kp"},
		{2560, 1440, false, "2Kp"},
		{1920, 1080, false, "1080p"},
		{1920, 1080, true, "1080i"},
		{1280, 720, false, "720p"},
		{1024, 576, false, "576p"},
		{854, 480, false, "480p"},
		{320, 240, false, "SDp"},
		{0, 0, false, ""},
	}
	for _, c := range cases {
		if got := Resolution_label(c.width, c.height, c.interlaced); got != c.want {
			t.Errorf("Resolution_label(%d, %d, %v) = %q, want %q", c.width, c.height, c.interlaced, got, c.want)
		}
	}
}

func Test_parse_hdr_and_10bit(t *testing.T) {
	input := `{
		"streams": [{
			"index": 0, "codec_type": "video", "codec_name": "hevc",
			"width": 3840, "height": 2160, "pix_fmt": "yuv420p10le",
			"field_order": "progressive", "avg_frame_rate": "24000/1001",
			"bits_per_raw_sample": "10", "color_transfer": "smpte2084"
		}],
		"format": {"format_name": "matroska,webm"}
	}`
	info, err := Parse_from_json("/hdr.mkv", []byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	v := info.Video
	if v == nil {
		t.Fatal("expected video")
	}
	if !v.Hdr {
		t.Error("expected HDR to be detected")
	}
	if v.Bit_depth != 10 {
		t.Errorf("bit depth = %d, want 10", v.Bit_depth)
	}
	if v.Resolution_label != "4Kp" {
		t.Errorf("label = %q, want 4Kp", v.Resolution_label)
	}
	const want_fps = 23.976
	if v.Frame_rate < want_fps-0.01 || v.Frame_rate > want_fps+0.01 {
		t.Errorf("frame rate = %v, want ~23.976", v.Frame_rate)
	}
}

func Test_parse_bit_depth_from_pixfmt(t *testing.T) {
	input := `{
		"streams": [{
			"index": 0, "codec_type": "video", "codec_name": "hevc",
			"width": 1920, "height": 1080, "pix_fmt": "yuv420p12le",
			"field_order": "progressive", "r_frame_rate": "0/0"
		}],
		"format": {}
	}`
	info, err := Parse_from_json("/x.mkv", []byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Video.Bit_depth != 12 {
		t.Errorf("bit depth = %d, want 12", info.Video.Bit_depth)
	}
	if info.Video.Frame_rate != 0 {
		t.Errorf("frame rate = %v, want 0 for 0/0", info.Video.Frame_rate)
	}
}

func Test_video_stream_dedup_warns(t *testing.T) {
	input := `{
		"streams": [
			{"index": 0, "codec_type": "video", "codec_name": "h264",
			 "width": 1280, "height": 720, "avg_frame_rate": "24/1"},
			{"index": 1, "codec_type": "video", "codec_name": "h264",
			 "width": 1280, "height": 720, "avg_frame_rate": "24/1"}
		],
		"format": {}
	}`
	info, err := Parse_from_json("/x.mkv", []byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(info.Warnings) != 1 {
		t.Errorf("warnings = %v, want 1", info.Warnings)
	}
	want := []string{"extra video stream 1 ignored"}
	if !reflect.DeepEqual(info.Warnings, want) {
		t.Errorf("warnings = %v, want %v", info.Warnings, want)
	}
}
