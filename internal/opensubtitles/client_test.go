package opensubtitles

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
		Api_key:    "test-key",
		User_agent: "Tomovee/test",
		Base_url:   server.URL,
		Http: httpclient.New(httpclient.Config{
			Rate_per_second: 1000,
			Burst:           1,
		}),
	})
}

func Test_search_by_hash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subtitles" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("moviehash"); got != "8e245d9679d31e12" {
			t.Errorf("moviehash = %q", got)
		}
		if got := r.Header.Get("Api-Key"); got != "test-key" {
			t.Errorf("Api-Key = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "Tomovee/test" {
			t.Errorf("User-Agent = %q", got)
		}
		_, _ = w.Write([]byte(`{
			"total_pages":1,"total_count":2,"page":1,
			"data":[
				{"id":"1","attributes":{"language":"en","download_count":500,
				 "feature_details":{"feature_id":1,"feature_type":"movie","year":1999,
				 "title":"The Matrix","movie_name":"The Matrix","imdb_id":133093,"tmdb_id":603}}},
				{"id":"2","attributes":{"language":"fr","download_count":100,
				 "feature_details":{"feature_id":1,"feature_type":"movie","year":1999,
				 "title":"The Matrix","movie_name":"The Matrix","imdb_id":133093,"tmdb_id":603}}}
			]
		}`))
	}))
	defer server.Close()

	features, err := new_test_client(t, server).Search_by_hash(context.Background(), "8e245d9679d31e12")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("features = %d, want 1 after de-duplication", len(features))
	}
	got := features[0]
	if got.Title != "The Matrix" || got.Year != 1999 {
		t.Errorf("feature = %+v", got)
	}
	if got.Imdb_id != "tt0133093" {
		t.Errorf("imdb id = %q, want tt0133093", got.Imdb_id)
	}
	if got.Tmdb_id != 603 || got.Feature_type != "movie" {
		t.Errorf("feature = %+v", got)
	}
}

func Test_search_by_hash_episode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"total_pages":1,"total_count":1,"page":1,
			"data":[
				{"id":"9","attributes":{"language":"en","download_count":10,
				 "feature_details":{"feature_type":"episode","year":2008,
				 "title":"Breaking Bad","movie_name":"Breaking Bad","imdb_id":903747,
				 "tmdb_id":1396,"season_number":1,"episode_number":1}}}
			]
		}`))
	}))
	defer server.Close()

	features, err := new_test_client(t, server).Search_by_hash(context.Background(), "abc")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(features) != 1 {
		t.Fatalf("features = %d", len(features))
	}
	got := features[0]
	if got.Feature_type != "episode" || got.Season != 1 || got.Episode != 1 {
		t.Errorf("feature = %+v", got)
	}
	if got.Imdb_id != "tt0903747" {
		t.Errorf("imdb id = %q, want tt0903747", got.Imdb_id)
	}
}

func Test_missing_api_key(t *testing.T) {
	client := New(Config{Api_key: "", Base_url: "http://example.invalid"})
	if _, err := client.Search_by_hash(context.Background(), "x"); !errors.Is(err, Err_no_api_key) {
		t.Errorf("err = %v, want Err_no_api_key", err)
	}
}

func Test_http_error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid api key"}`))
	}))
	defer server.Close()

	if _, err := new_test_client(t, server).Search_by_hash(context.Background(), "x"); err == nil {
		t.Fatal("expected error for 401")
	}
}

func Test_imdb_id_from_number(t *testing.T) {
	cases := map[int64]string{
		0:       "",
		-5:      "",
		133093:  "tt0133093",
		903747:  "tt0903747",
		1234567: "tt1234567",
	}
	for in, want := range cases {
		if got := Imdb_id_from_number(in); got != want {
			t.Errorf("Imdb_id_from_number(%d) = %q, want %q", in, got, want)
		}
	}
}

func Test_features_fallback_title(t *testing.T) {
	resp := subtitles_response{
		Data: []subtitle_item{{
			Attributes: subtitle_attributes{
				Feature_details: feature_details{Movie_name: "Fallback Name"},
			},
		}},
	}
	features := features_from_response(resp)
	if len(features) != 1 || features[0].Title != "Fallback Name" {
		t.Errorf("features = %+v", features)
	}
}
