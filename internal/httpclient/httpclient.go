// Package httpclient provides a small JSON-over-HTTP client with request rate
// limiting and retry/backoff, shared by the external API clients.
package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

const max_body_bytes = 4 << 20

// Config configures a Client. Zero values fall back to sensible defaults.
type Config struct {
	User_agent       string
	Rate_per_second  float64
	Burst            int
	Max_retries      int
	Timeout          time.Duration
	Retry_base_delay time.Duration
	Logger           *slog.Logger
}

// Client is a rate-limited, retrying JSON HTTP client.
type Client struct {
	http             *http.Client
	limiter          *rate.Limiter
	max_retries      int
	retry_base_delay time.Duration
	user_agent       string
	logger           *slog.Logger
}

// New builds a Client from cfg.
func New(cfg Config) *Client {
	if cfg.Rate_per_second <= 0 {
		cfg.Rate_per_second = 4
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 1
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.Retry_base_delay <= 0 {
		cfg.Retry_base_delay = 300 * time.Millisecond
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		http:             &http.Client{Timeout: cfg.Timeout},
		limiter:          rate.NewLimiter(rate.Limit(cfg.Rate_per_second), cfg.Burst),
		max_retries:      cfg.Max_retries,
		retry_base_delay: cfg.Retry_base_delay,
		user_agent:       cfg.User_agent,
		logger:           logger,
	}
}

// Get_json performs a GET request against url, sending headers, and decodes a
// 2xx JSON response into out (out may be nil). It rate-limits calls and retries
// on network errors, HTTP 429, and 5xx responses, honouring Retry-After.
func (c *Client) Get_json(ctx context.Context, url string, headers map[string]string, out any) error {
	var last_err error
	for attempt := 0; attempt <= c.max_retries; attempt++ {
		if err := c.wait_before_retry(ctx, attempt, last_err); err != nil {
			return err
		}
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}
		req, err := c.create_get_request(ctx, url, headers)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last_err = err
			continue
		}
		body, read_err := read_limited_body(resp)
		if read_err != nil {
			last_err = read_err
			continue
		}
		retry, err := handle_response(ctx, resp, body, out)
		if !retry {
			return err
		}
		last_err = err
	}
	return fmt.Errorf("httpclient: request failed after %d attempt(s): %w", c.max_retries+1, last_err)
}

// wait_before_retry logs and sleeps through the backoff delay before a retry;
// the first attempt passes straight through.
func (c *Client) wait_before_retry(ctx context.Context, attempt int, last_err error) error {
	if attempt == 0 {
		return nil
	}
	delay := c.backoff(attempt)
	c.logger.Debug("retrying http request", "attempt", attempt, "delay", delay, "error", last_err)
	return sleep_ctx(ctx, delay)
}

func (c *Client) create_get_request(ctx context.Context, url string, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.user_agent != "" {
		req.Header.Set("User-Agent", c.user_agent)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req, nil
}

// read_limited_body reads at most max_body_bytes from resp.Body and always
// closes it.
func read_limited_body(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, max_body_bytes))
}

// handle_response inspects a completed response. A 2xx decodes into out and
// ends the request (retry=false, err=nil). 429/5xx honours Retry-After and
// reports a retryable failure (retry=true). Anything else fails permanently.
func handle_response(ctx context.Context, resp *http.Response, body []byte, out any) (retry bool, err error) {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil {
			return false, nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return false, fmt.Errorf("httpclient: decode response: %w", err)
		}
		return false, nil
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		retry_err := fmt.Errorf("http %d: %s", resp.StatusCode, snippet(body))
		if wait := parse_retry_after(resp.Header.Get("Retry-After")); wait > 0 {
			if err := sleep_ctx(ctx, wait); err != nil {
				return false, err
			}
		}
		return true, retry_err
	}
	return false, fmt.Errorf("httpclient: http %d: %s", resp.StatusCode, snippet(body))
}

// backoff returns an exponential delay for the given (1-based) attempt.
func (c *Client) backoff(attempt int) time.Duration {
	delay := c.retry_base_delay << (attempt - 1)
	const cap_delay = 10 * time.Second
	if delay > cap_delay || delay <= 0 {
		delay = cap_delay
	}
	return delay
}

func sleep_ctx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// parse_retry_after understands both delay-seconds and HTTP-date forms.
func parse_retry_after(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

func snippet(body []byte) string {
	const max_body_size = 200
	if len(body) > max_body_size {
		return string(body[:max_body_size]) + "…"
	}
	return string(body)
}
