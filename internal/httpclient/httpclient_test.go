package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := test_client().Get_json(ctx, server.URL, nil, nil); err == nil {
		t.Fatal("expected context error")
	}
}

// cancel_on_eof_body cancels a context once the response body has been read to
// completion. It pins the retry loop's ordering without timing races: the
// first response is consumed in full, then the cancellation is in effect for
// whatever the client does afterwards (a Retry-After sleep or the next
// attempt's backoff sleep).
type cancel_on_eof_body struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancel_on_eof_body) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.cancel()
	}
	return n, err
}

// cancel_on_eof_transport wraps a real transport so every response body
// cancels the request context at EOF.
type cancel_on_eof_transport struct {
	base   http.RoundTripper
	cancel context.CancelFunc
}

func (t *cancel_on_eof_transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &cancel_on_eof_body{ReadCloser: resp.Body, cancel: t.cancel}
	return resp, nil
}

func test_client_with_cancel_on_eof(cancel context.CancelFunc) *Client {
	client := test_client()
	client.http.Transport = &cancel_on_eof_transport{base: http.DefaultTransport, cancel: cancel}
	return client
}

// A retryable 5xx whose context dies as the first response is consumed must
// surface the cancellation from the retry backoff, and the second attempt must
// never reach the server.
func Test_get_json_backoff_sleep_returns_context_error(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := test_client_with_cancel_on_eof(cancel)

	err := client.Get_json(ctx, server.URL, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled from the retry backoff", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (cancelled before the retry)", got)
	}
}

// A 429 whose context dies before the Retry-After wait completes aborts
// immediately instead of retrying, so a shutdown cannot be held up by a rate
// limit sleep.
func Test_get_json_retry_after_sleep_returns_context_error(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := test_client_with_cancel_on_eof(cancel)

	err := client.Get_json(ctx, server.URL, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled from the Retry-After wait", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry after a dead wait)", got)
	}
}

// Once the attempts run out, the last failure is wrapped with the attempt
// count so the caller can tell a persistent outage from a single bad request.
func Test_get_json_reports_failure_after_retries_exhausted(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer server.Close()

	client := New(Config{
		Rate_per_second:  1000,
		Burst:            1,
		Max_retries:      1,
		Retry_base_delay: time.Millisecond,
	})
	err := client.Get_json(context.Background(), server.URL, nil, nil)
	if err == nil {
		t.Fatal("expected error after retries were exhausted")
	}
	if !strings.Contains(err.Error(), "after 2 attempt(s)") {
		t.Errorf("err = %v, want the attempt count in the message", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

// A 2xx with a body that is not valid JSON fails permanently: a decode error
// is a contract violation, not a transient condition, so no retry is made.
func Test_get_json_invalid_json_fails_permanently(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte("this is not json"))
	}))
	defer server.Close()

	var out struct {
		Ok bool `json:"ok"`
	}
	err := test_client().Get_json(context.Background(), server.URL, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("err = %v, want a decode failure", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (decode errors are permanent)", got)
	}
}

// A URL that cannot even be parsed must fail before any request is sent.
func Test_create_get_request_invalid_url(t *testing.T) {
	req, err := test_client().create_get_request(context.Background(), "http://example.com/%zz", nil)
	if err == nil {
		t.Fatal("expected error for an invalid URL escape")
	}
	if req != nil {
		t.Errorf("request = %+v, want nil on failure", req)
	}
}

// Long upstream error bodies are truncated to a fixed prefix, so log lines and
// API errors stay bounded no matter what the server sends.
func Test_snippet_truncates_long_body(t *testing.T) {
	body := strings.Repeat("x", 300)
	got := snippet([]byte(body))
	if !strings.HasPrefix(got, body[:200]) {
		t.Errorf("snippet does not start with the first %d bytes: %q", 200, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("snippet = %q, want the truncation ellipsis", got)
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
