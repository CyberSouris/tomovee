package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cybersouris/tomovee/internal/httpclient"
)

func new_test_client(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	return New(Config{
		Api_key:  "test-key",
		Base_url: server.URL,
		Language: "en-US",
		Http: httpclient.New(httpclient.Config{
			Rate_per_second: 1000,
			Burst:           1,
		}),
	})
}

func Test_search_movie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/movie" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("query") != "matrix" || q.Get("year") != "1999" {
			t.Errorf("query params = %v", q)
		}
		if q.Get("api_key") != "test-key" || q.Get("language") != "en-US" {
			t.Errorf("auth/lang params = %v", q)
		}
		_, _ = w.Write([]byte(`{"page":1,"results":[
			{"id":603,"title":"The Matrix","original_title":"The Matrix",
			 "release_date":"1999-03-31","overview":"Sci-fi","poster_path":"/m.jpg",
			 "vote_average":8.2,"vote_count":20000,"genre_ids":[28,878]}
		]}`))
	}))
	defer server.Close()

	results, err := new_test_client(t, server).Search_movie(context.Background(), "matrix", 1999)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	got := results[0]
	if got.Id != 603 || got.Title != "The Matrix" || got.Vote_average != 8.2 {
		t.Errorf("result = %+v", got)
	}
	if Year_from_date(got.Release_date) != 1999 {
		t.Errorf("year = %d, want 1999", Year_from_date(got.Release_date))
	}
}

func Test_search_tv(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/tv" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("first_air_date_year") != "2008" {
			t.Errorf("first_air_date_year = %q", r.URL.Query().Get("first_air_date_year"))
		}
		_, _ = w.Write([]byte(`{"page":1,"results":[
			{"id":1396,"name":"Breaking Bad","first_air_date":"2008-01-20"}
		]}`))
	}))
	defer server.Close()

	results, err := new_test_client(t, server).Search_tv(context.Background(), "breaking bad", 2008)
	if err != nil {
		t.Fatalf("search tv: %v", err)
	}
	if len(results) != 1 || results[0].Id != 1396 || results[0].Name != "Breaking Bad" {
		t.Fatalf("results = %+v", results)
	}
}

func Test_movie_details(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/movie/603" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"id":603,"imdb_id":"tt0133093","title":"The Matrix","runtime":136,
			"genres":[{"id":28,"name":"Action"},{"id":878,"name":"Science Fiction"}]
		}`))
	}))
	defer server.Close()

	details, err := new_test_client(t, server).Movie_details(context.Background(), 603)
	if err != nil {
		t.Fatalf("details: %v", err)
	}
	if details.Imdb_id != "tt0133093" || details.Runtime != 136 {
		t.Errorf("details = %+v", details)
	}
	if len(details.Genres) != 2 || details.Genres[1].Name != "Science Fiction" {
		t.Errorf("genres = %+v", details.Genres)
	}
}

func Test_tv_details(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/1396" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":1396,"name":"Breaking Bad","number_of_seasons":5,"number_of_episodes":62}`))
	}))
	defer server.Close()

	details, err := new_test_client(t, server).Tv_details(context.Background(), 1396)
	if err != nil {
		t.Fatalf("tv details: %v", err)
	}
	if details.Number_of_seasons != 5 || details.Number_of_episodes != 62 {
		t.Errorf("details = %+v", details)
	}
}

func Test_find_by_imdb(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/find/tt0133093" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("external_source") != "imdb_id" {
			t.Errorf("external_source = %q", r.URL.Query().Get("external_source"))
		}
		_, _ = w.Write([]byte(`{"movie_results":[{"id":603,"title":"The Matrix"}],"tv_results":[]}`))
	}))
	defer server.Close()

	result, err := new_test_client(t, server).Find_by_imdb(context.Background(), "tt0133093")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(result.Movie_results) != 1 || result.Movie_results[0].Id != 603 {
		t.Errorf("result = %+v", result)
	}
}

func Test_missing_api_key(t *testing.T) {
	client := New(Config{Api_key: "", Base_url: "http://example.invalid"})
	if _, err := client.Search_movie(context.Background(), "x", 0); !errors.Is(err, Err_no_api_key) {
		t.Errorf("err = %v, want Err_no_api_key", err)
	}
}

func Test_http_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status_message":"not found"}`))
	}))
	defer server.Close()

	if _, err := new_test_client(t, server).Movie_details(context.Background(), 0); err == nil {
		t.Fatal("expected error for 404")
	}
}

func Test_poster_url(t *testing.T) {
	if got := Poster_url("", "w500"); got != "" {
		t.Errorf("empty poster = %q", got)
	}
	if got := Poster_url("/abc.jpg", "w500"); got != "https://image.tmdb.org/t/p/w500/abc.jpg" {
		t.Errorf("poster url = %q", got)
	}
}
