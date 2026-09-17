package opensubtitles

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/cybersouris/tomovee/internal/httpclient"
)

// Default_base_url is the OpenSubtitles REST API v1 root.
const Default_base_url = "https://api.opensubtitles.com/api/v1"

// Err_no_api_key is returned by API methods when no key is configured.
var Err_no_api_key = errors.New("opensubtitles: api key is not configured")

// Config configures a Client.
type Config struct {
	Api_key    string
	User_agent string
	Base_url   string
	Http       *httpclient.Client
}

// Client talks to the OpenSubtitles REST API.
type Client struct {
	api_key    string
	user_agent string
	base_url   string
	http       *httpclient.Client
}

// New builds a Client from cfg.
func New(cfg Config) *Client {
	base_url := cfg.Base_url
	if base_url == "" {
		base_url = Default_base_url
	}
	user_agent := cfg.User_agent
	if user_agent == "" {
		user_agent = "Tomovee/0.1 by Cyber Souris"
	}
	http_client := cfg.Http
	if http_client == nil {
		http_client = httpclient.New(httpclient.Config{
			User_agent:      user_agent,
			Rate_per_second: 1,
			Burst:           1,
			Max_retries:     3,
		})
	}
	return &Client{
		api_key:    cfg.Api_key,
		user_agent: user_agent,
		base_url:   base_url,
		http:       http_client,
	}
}

// Feature is a normalized movie/series/episode identity returned by a search.
type Feature struct {
	Title          string
	Year           int
	Imdb_id        string
	Tmdb_id        int
	Feature_type   string // "movie", "episode", or "tvshow"
	Season         int
	Episode        int
	Language       string
	Download_count int
}

// Search_by_hash looks up subtitle records matching an OpenSubtitles file hash
// and returns the distinct features they refer to.
func (c *Client) Search_by_hash(ctx context.Context, hash string) ([]Feature, error) {
	params := url.Values{}
	params.Set("moviehash", hash)
	var resp subtitles_response
	if err := c.get(ctx, "/subtitles", params, &resp); err != nil {
		return nil, err
	}
	return features_from_response(resp), nil
}

func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	if c.api_key == "" {
		return Err_no_api_key
	}
	headers := map[string]string{
		"Api-Key":    c.api_key,
		"User-Agent": c.user_agent,
	}
	target := c.base_url + path + "?" + params.Encode()
	return c.http.Get_json(ctx, target, headers, out)
}

// features_from_response flattens the API response, de-duplicating repeated
// features that appear once per available subtitle language.
func features_from_response(resp subtitles_response) []Feature {
	seen := make(map[string]bool)
	var out []Feature
	for _, item := range resp.Data {
		details := item.Attributes.Feature_details
		feature := Feature{
			Title:          details.Title,
			Year:           details.Year,
			Imdb_id:        Imdb_id_from_number(details.Imdb_id),
			Tmdb_id:        details.Tmdb_id,
			Feature_type:   details.Feature_type,
			Season:         details.Season_number,
			Episode:        details.Episode_number,
			Language:       item.Attributes.Language,
			Download_count: item.Attributes.Download_count,
		}
		if feature.Title == "" {
			feature.Title = details.Movie_name
		}
		key := feature.Imdb_id
		if key == "" {
			key = fmt.Sprintf("%s|%d|%s|%d|%d", feature.Title, feature.Year, feature.Feature_type, feature.Season, feature.Episode)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, feature)
	}
	return out
}

// Imdb_id_from_number formats OpenSubtitles' numeric IMDb id (e.g. 133093) as
// the canonical "tt0133093" form. Returns "" for non-positive input.
func Imdb_id_from_number(n int64) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("tt%07d", n)
}

type subtitles_response struct {
	Total_pages int             `json:"total_pages"`
	Total_count int             `json:"total_count"`
	Page        int             `json:"page"`
	Data        []subtitle_item `json:"data"`
}

type subtitle_item struct {
	Id         string              `json:"id"`
	Attributes subtitle_attributes `json:"attributes"`
}

type subtitle_attributes struct {
	Language        string          `json:"language"`
	Download_count  int             `json:"download_count"`
	Feature_details feature_details `json:"feature_details"`
}

type feature_details struct {
	Feature_id     int    `json:"feature_id"`
	Feature_type   string `json:"feature_type"`
	Year           int    `json:"year"`
	Title          string `json:"title"`
	Movie_name     string `json:"movie_name"`
	Imdb_id        int64  `json:"imdb_id"`
	Tmdb_id        int    `json:"tmdb_id"`
	Season_number  int    `json:"season_number"`
	Episode_number int    `json:"episode_number"`
}
