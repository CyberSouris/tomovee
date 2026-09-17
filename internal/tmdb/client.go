// Package tmdb is a small client for The Movie Database (TMDB) v3 API.
package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/cybersouris/tomovee/internal/httpclient"
)

// Default_base_url is the TMDB v3 API root.
const Default_base_url = "https://api.themoviedb.org/3"

// Err_no_api_key is returned by API methods when no key is configured.
var Err_no_api_key = errors.New("tmdb: api key is not configured")

// Config configures a Client.
type Config struct {
	Api_key  string
	Base_url string
	Language string
	Http     *httpclient.Client
}

// Client talks to the TMDB API.
type Client struct {
	api_key  string
	base_url string
	language string
	http     *httpclient.Client
}

// New builds a Client from cfg.
func New(cfg Config) *Client {
	base_url := strings.TrimRight(cfg.Base_url, "/")
	if base_url == "" {
		base_url = Default_base_url
	}
	language := cfg.Language
	if language == "" {
		language = "en-US"
	}
	http_client := cfg.Http
	if http_client == nil {
		http_client = httpclient.New(httpclient.Config{
			User_agent:      "Tomovee/0.1",
			Rate_per_second: 4,
			Burst:           1,
			Max_retries:     3,
		})
	}
	return &Client{
		api_key:  cfg.Api_key,
		base_url: base_url,
		language: language,
		http:     http_client,
	}
}

// Genre is a TMDB genre id/name pair.
type Genre struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

// Movie_search_result is one entry of a movie search response.
type Movie_search_result struct {
	Id             int     `json:"id"`
	Title          string  `json:"title"`
	Original_title string  `json:"original_title"`
	Release_date   string  `json:"release_date"`
	Overview       string  `json:"overview"`
	Poster_path    string  `json:"poster_path"`
	Vote_average   float64 `json:"vote_average"`
	Vote_count     int     `json:"vote_count"`
	Genre_ids      []int   `json:"genre_ids"`
	Popularity     float64 `json:"popularity"`
}

// Movie_details is the full movie record.
type Movie_details struct {
	Id             int     `json:"id"`
	Imdb_id        string  `json:"imdb_id"`
	Title          string  `json:"title"`
	Original_title string  `json:"original_title"`
	Release_date   string  `json:"release_date"`
	Overview       string  `json:"overview"`
	Poster_path    string  `json:"poster_path"`
	Vote_average   float64 `json:"vote_average"`
	Vote_count     int     `json:"vote_count"`
	Runtime        int     `json:"runtime"`
	Genres         []Genre `json:"genres"`
	Status         string  `json:"status"`
	Tagline        string  `json:"tagline"`
}

// Tv_search_result is one entry of a TV search response.
type Tv_search_result struct {
	Id             int     `json:"id"`
	Name           string  `json:"name"`
	Original_name  string  `json:"original_name"`
	First_air_date string  `json:"first_air_date"`
	Overview       string  `json:"overview"`
	Poster_path    string  `json:"poster_path"`
	Vote_average   float64 `json:"vote_average"`
	Vote_count     int     `json:"vote_count"`
	Genre_ids      []int   `json:"genre_ids"`
	Popularity     float64 `json:"popularity"`
}

// Tv_details is the full TV show record.
type Tv_details struct {
	Id                 int     `json:"id"`
	Imdb_id            string  `json:"imdb_id"`
	Name               string  `json:"name"`
	Original_name      string  `json:"original_name"`
	First_air_date     string  `json:"first_air_date"`
	Last_air_date      string  `json:"last_air_date"`
	Overview           string  `json:"overview"`
	Poster_path        string  `json:"poster_path"`
	Vote_average       float64 `json:"vote_average"`
	Vote_count         int     `json:"vote_count"`
	Number_of_seasons  int     `json:"number_of_seasons"`
	Number_of_episodes int     `json:"number_of_episodes"`
	Genres             []Genre `json:"genres"`
	Status             string  `json:"status"`
}

// Find_result is the response of a find-by-external-id lookup.
type Find_result struct {
	Movie_results []Movie_search_result `json:"movie_results"`
	Tv_results    []Tv_search_result    `json:"tv_results"`
}

type movie_search_response struct {
	Page    int                   `json:"page"`
	Results []Movie_search_result `json:"results"`
}

type tv_search_response struct {
	Page    int                `json:"page"`
	Results []Tv_search_result `json:"results"`
}

// Search_movie searches for movies by title, optionally restricted to a year.
func (c *Client) Search_movie(ctx context.Context, query string, year int) ([]Movie_search_result, error) {
	params := url.Values{}
	params.Set("query", query)
	if year > 0 {
		params.Set("year", strconv.Itoa(year))
	}
	var resp movie_search_response
	if err := c.get(ctx, "/search/movie", params, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// Search_tv searches for TV shows by name, optionally restricted to a first
// air year.
func (c *Client) Search_tv(ctx context.Context, query string, year int) ([]Tv_search_result, error) {
	params := url.Values{}
	params.Set("query", query)
	if year > 0 {
		params.Set("first_air_date_year", strconv.Itoa(year))
	}
	var resp tv_search_response
	if err := c.get(ctx, "/search/tv", params, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// Movie_details fetches the full record for a movie id.
func (c *Client) Movie_details(ctx context.Context, id int) (*Movie_details, error) {
	var details Movie_details
	if err := c.get(ctx, fmt.Sprintf("/movie/%d", id), url.Values{}, &details); err != nil {
		return nil, err
	}
	return &details, nil
}

// Tv_details fetches the full record for a TV id.
func (c *Client) Tv_details(ctx context.Context, id int) (*Tv_details, error) {
	var details Tv_details
	if err := c.get(ctx, fmt.Sprintf("/tv/%d", id), url.Values{}, &details); err != nil {
		return nil, err
	}
	return &details, nil
}

// Find_by_imdb resolves an IMDb id (e.g. "tt0133093") to TMDB records.
func (c *Client) Find_by_imdb(ctx context.Context, imdb_id string) (*Find_result, error) {
	params := url.Values{}
	params.Set("external_source", "imdb_id")
	var result Find_result
	if err := c.get(ctx, "/find/"+url.PathEscape(imdb_id), params, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	if c.api_key == "" {
		return Err_no_api_key
	}
	params.Set("api_key", c.api_key)
	if c.language != "" {
		params.Set("language", c.language)
	}
	target := c.base_url + path + "?" + params.Encode()
	return c.http.Get_json(ctx, target, nil, out)
}

// Year_from_date extracts a four-digit year from a TMDB date string such as
// "1999-03-31". Returns 0 when the date is empty or unparseable.
func Year_from_date(date string) int {
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return year
}

// Poster_url builds the full URL for a TMDB poster path at the given size
// (e.g. "w500"). Returns "" for an empty path.
func Poster_url(poster_path, size string) string {
	if poster_path == "" {
		return ""
	}
	if size == "" {
		size = "original"
	}
	return "https://image.tmdb.org/t/p/" + size + poster_path
}
