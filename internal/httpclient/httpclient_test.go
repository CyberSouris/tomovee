package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func test_client() *Client {
	return New(Config{
		User_agent:       "Tomovee/test",
		Rate_per_second:  1000,
		Burst:            1,
		Max_retries:      3,
		Retry_base_delay: time.Millisecond,
	})
}

func Test_get_json_success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "Tomovee/test" {
			t.Errorf("user agent = %q", got)
		}
		if got := r.Header.Get("X-Test"); got != "yes" {
			t.Errorf("custom header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"ok","count":2}`))
	}))
	defer server.Close()

	var out struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	err := test_client().Get_json(context.Background(), server.URL, map[string]string{"X-Test": "yes"}, &out)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if out.Name != "ok" || out.Count != 2 {
		t.Errorf("decoded = %+v", out)
	}
}

func Test_get_json_retries_5xx(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	var out struct {
		Ok bool `json:"ok"`
	}
	if err := test_client().Get_json(context.Background(), server.URL, nil, &out); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !out.Ok {
		t.Error("expected decoded ok=true")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func Test_get_json_retries_429_with_retry_after(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	if err := test_client().Get_json(context.Background(), server.URL, nil, nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

func Test_get_json_no_retry_on_4xx(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	err := test_client().Get_json(context.Background(), server.URL, nil, nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", got)
	}
}

func Test_get_json_respects_context_cancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := test_client().Get_json(ctx, server.URL, nil, nil); err == nil {
		t.Fatal("expected context error")
	}
}

func Test_parse_retry_after(t *testing.T) {
	if got := parse_retry_after("5"); got != 5*time.Second {
		t.Errorf("seconds = %v", got)
	}
	if got := parse_retry_after(""); got != 0 {
		t.Errorf("empty = %v", got)
	}
	if got := parse_retry_after("-3"); got != 0 {
		t.Errorf("negative = %v", got)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if got := parse_retry_after(future); got <= 0 {
		t.Errorf("http-date = %v, want > 0", got)
	}
}
