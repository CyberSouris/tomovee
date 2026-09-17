// Package metadata extracts technical metadata from media files by invoking
// ffprobe and parsing its JSON output.
package metadata

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// File_info holds the technical metadata extracted from one media file. It is
// the product of the ffprobe wrapper; scanner layer decorates it with paths and
// filesystem facts.
type File_info struct {
	Path             string
	Container        string
	Duration_seconds float64
	Bit_rate         int64
	Size_bytes       int64
	Video            *Video_stream_info
	Audio            []Audio_stream_info
	Subtitles        []Subtitle_stream_info
	// Warnings collects non-fatal per-stream problems encountered while
	// parsing, so one bad stream never fails the whole file.
	Warnings []string
}

// Video_stream_info describes the primary video track.
type Video_stream_info struct {
	Codec            string
	Width            int
	Height           int
	Resolution_label string
	Hdr              bool
	Bit_depth        int
	Frame_rate       float64
}

// Audio_stream_info describes a single audio track.
type Audio_stream_info struct {
	Language string
	Codec    string
	Channels int
}

// Subtitle_stream_info describes a single subtitle track.
type Subtitle_stream_info struct {
	Language string
	Format   string
}

// Parse_from_json parses raw ffprobe JSON (the output of
// `ffprobe -show_format -show_streams -of json`) into a File_info for path.
func Parse_from_json(path string, data []byte) (*File_info, error) {
	var out ffprobe_output
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse ffprobe json: %w", err)
	}
	return build_file_info(path, &out), nil
}

// Resolution_label maps a frame height to a human label such as "1080p".
// Interlaced content (top/bottom-field-first) is marked with a trailing "i".
func Resolution_label(width, height int, interlaced bool) string {
	suffix := "p"
	if interlaced {
		suffix = "i"
	}
	switch {
	case height >= 4320:
		return fmt.Sprintf("8K%s", suffix)
	case height >= 2160:
		return fmt.Sprintf("4K%s", suffix)
	case height >= 1440:
		return fmt.Sprintf("2K%s", suffix)
	case height >= 1080:
		return "1080" + suffix
	case height >= 720:
		return "720" + suffix
	case height >= 576:
		return "576" + suffix
	case height >= 480:
		return "480" + suffix
	case height > 0:
		return "SD" + suffix
	}
	return ""
}

func build_file_info(path string, out *ffprobe_output) *File_info {
	info := &File_info{Path: path, Container: out.Format.Format_name}
	info.Duration_seconds = parse_float(out.Format.Duration)
	info.Bit_rate = parse_int64(out.Format.Bit_rate)
	info.Size_bytes = parse_int64(out.Format.Size)

	for _, s := range out.Streams {
		switch s.Codec_type {
		case "video":
			if info.Video != nil {
				info.Warnings = append(info.Warnings,
					fmt.Sprintf("extra video stream %d ignored", s.Index))
				continue
			}
			info.Video = video_from_stream(s)
		case "audio":
			info.Audio = append(info.Audio, Audio_stream_info{
				Language: s.Tags.Language,
				Codec:    s.Codec_name,
				Channels: s.Channels,
			})
		case "subtitle":
			info.Subtitles = append(info.Subtitles, Subtitle_stream_info{
				Language: s.Tags.Language,
				Format:   s.Codec_name,
			})
		default:
			info.Warnings = append(info.Warnings,
				fmt.Sprintf("ignored unknown stream type %q (index %d)", s.Codec_type, s.Index))
		}
	}
	return info
}

func video_from_stream(s ffprobe_stream) *Video_stream_info {
	v := &Video_stream_info{
		Codec:      s.Codec_name,
		Width:      s.Width,
		Height:     s.Height,
		Hdr:        is_hdr(s),
		Bit_depth:  bit_depth(s),
		Frame_rate: parse_frame_rate(s),
	}
	v.Resolution_label = Resolution_label(s.Width, s.Height, is_interlaced(s.Field_order))
	return v
}

// is_interlaced reports whether the stream field order (tt/bb) indicates
// interlaced scanning.
func is_interlaced(field_order string) bool {
	if field_order == "" {
		return false
	}
	return strings.HasPrefix(field_order, "tt") || strings.HasPrefix(field_order, "bb")
}

// is_hdr heuristically detects HDR via the color transfer characteristic
// (PQ / HLG curves used by HDR10 and HLG).
func is_hdr(s ffprobe_stream) bool {
	switch s.Color_transfer {
	case "smpte2084", "arib-std-b67":
		return true
	}
	return false
}

// bit_depth resolves the sample bit depth from bits_per_raw_sample, falling
// back to a "p10"/"p12"/"p16" suffix in the pixel format.
func bit_depth(s ffprobe_stream) int {
	if s.Bits_per_raw_sample != "" {
		if depth, err := strconv.Atoi(s.Bits_per_raw_sample); err == nil {
			return depth
		}
	}
	switch {
	case strings.Contains(s.Pix_fmt, "p10"):
		return 10
	case strings.Contains(s.Pix_fmt, "p12"):
		return 12
	case strings.Contains(s.Pix_fmt, "p16"):
		return 16
	}
	return 0
}

// parse_frame_rate resolves the average frame rate ("24000/1001"), falling
// back to the real frame rate. Returns 0 when neither parses.
func parse_frame_rate(s ffprobe_stream) float64 {
	if rate := parse_rational(s.Avg_frame_rate); rate > 0 {
		return rate
	}
	return parse_rational(s.R_frame_rate)
}

func parse_rational(value string) float64 {
	if value == "" {
		return 0
	}
	if !strings.Contains(value, "/") {
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0
		}
		return f
	}
	parts := strings.SplitN(value, "/", 2)
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

func parse_float(value string) float64 {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return f
}

func parse_int64(value string) int64 {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ffprobe_output mirrors the JSON shape produced by
// `ffprobe -v error -show_format -show_streams -of json`.
type ffprobe_output struct {
	Streams []ffprobe_stream `json:"streams"`
	Format  ffprobe_format   `json:"format"`
}

type ffprobe_format struct {
	Format_name string `json:"format_name"`
	Duration    string `json:"duration"`
	Bit_rate    string `json:"bit_rate"`
	Size        string `json:"size"`
}

type ffprobe_stream struct {
	Index               int          `json:"index"`
	Codec_name          string       `json:"codec_name"`
	Codec_type          string       `json:"codec_type"`
	Width               int          `json:"width"`
	Height              int          `json:"height"`
	Pix_fmt             string       `json:"pix_fmt"`
	Field_order         string       `json:"field_order"`
	R_frame_rate        string       `json:"r_frame_rate"`
	Avg_frame_rate      string       `json:"avg_frame_rate"`
	Bits_per_raw_sample string       `json:"bits_per_raw_sample"`
	Color_transfer      string       `json:"color_transfer"`
	Channels            int          `json:"channels"`
	Tags                ffprobe_tags `json:"tags"`
}

type ffprobe_tags struct {
	Language string `json:"language"`
}
