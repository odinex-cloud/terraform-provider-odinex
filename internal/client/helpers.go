package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the public Odinex Cloud API.
const DefaultEndpoint = "https://api.odinex.cloud/v1"

// Odinex wraps the generated client with the settings the provider needs.
type Odinex struct {
	*ClientWithResponses
	PollInterval time.Duration
}

// Config configures New.
type Config struct {
	Endpoint     string
	Token        string
	UserAgent    string
	PollInterval time.Duration
	HTTPClient   *http.Client
}

// New builds an authenticated API client.
func New(cfg Config) (*Odinex, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	// No overall http.Client timeout: it would include the sleeps of the retry
	// transport (Retry-After can be minutes). Each attempt is bounded by the
	// response header timeout instead, and every call by its context.
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	base := hc.Transport
	if base == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 60 * time.Second
		base = t
	}
	hc.Transport = &retryTransport{next: base, maxRetries: 8}
	c, err := NewClientWithResponses(strings.TrimRight(cfg.Endpoint, "/"),
		WithHTTPClient(hc),
		WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			if cfg.Token != "" {
				req.Header.Set("Authorization", "Bearer "+cfg.Token)
			}
			if cfg.UserAgent != "" {
				req.Header.Set("User-Agent", cfg.UserAgent)
			}
			return nil
		}))
	if err != nil {
		return nil, err
	}
	return &Odinex{ClientWithResponses: c, PollInterval: cfg.PollInterval}, nil
}

// APIError is a non-2xx answer of the API, decoded from the Error envelope.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Details    map[string]any
	Method     string
	URL        string
	Body       string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.URL, e.StatusCode)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	} else if e.Body != "" {
		msg += ": " + e.Body
	}
	if len(e.Details) > 0 {
		if b, err := json.Marshal(e.Details); err == nil {
			msg += " " + string(b)
		}
	}
	return msg
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.StatusCode == http.StatusNotFound || ae.Code == "not_found")
}

// IsCode reports whether err is an API error with the given code.
func IsCode(err error, code string) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == code
}

// Check turns a transport error or a non-2xx response into an error.
// Use it as client.Check(err, resp.HTTPResponse, resp.Body).
func Check(err error, hr *http.Response, body []byte) error {
	if err != nil {
		return err
	}
	if hr == nil {
		return errors.New("no response from the API")
	}
	if hr.StatusCode >= 200 && hr.StatusCode < 300 {
		return nil
	}
	ae := &APIError{StatusCode: hr.StatusCode}
	if hr.Request != nil {
		ae.Method = hr.Request.Method
		ae.URL = hr.Request.URL.Path
	}
	var env ErrorResponse
	if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
		ae.Code = env.Error.Code
		ae.Message = env.Error.Message
		if env.Error.Details != nil {
			ae.Details = *env.Error.Details
		}
	} else {
		ae.Body = strings.TrimSpace(string(body))
		if len(ae.Body) > 500 {
			ae.Body = ae.Body[:500]
		}
	}
	return ae
}

// ActionFailedError is returned when an action finished with status error.
type ActionFailedError struct {
	Action Action
}

func (e *ActionFailedError) Error() string {
	code, msg := "unknown", "action failed"
	if e.Action.Error != nil {
		code, msg = e.Action.Error.Code, e.Action.Error.Message
	}
	return fmt.Sprintf("action %d (%s) failed: %s: %s", e.Action.Id, e.Action.Command, code, msg)
}

// WaitForAction polls GET /actions/{id} until the action is no longer
// running. An action that ends in error is returned as *ActionFailedError.
func (c *Odinex) WaitForAction(ctx context.Context, a *Action) error {
	if a == nil {
		return nil
	}
	cur := *a
	for {
		switch cur.Status {
		case "success":
			return nil
		case "error":
			return &ActionFailedError{Action: cur}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for action %d (%s): %w", cur.Id, cur.Command, ctx.Err())
		case <-time.After(c.PollInterval):
		}
		r, err := Result(c.GetActionWithResponse(ctx, cur.Id))
		if err != nil {
			return fmt.Errorf("polling action %d: %w", cur.Id, err)
		}
		if r.JSON200 == nil {
			return fmt.Errorf("polling action %d: unexpected response %s", cur.Id, string(r.Body))
		}
		cur = r.JSON200.Action
	}
}

// Result checks a generated *XxxResponse: transport errors and non-2xx
// answers become errors. Use it as client.Result(c.GetServerWithResponse(ctx, id)).
func Result[T any](r *T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("no response from the API")
	}
	v := reflect.ValueOf(r).Elem()
	hr, _ := v.FieldByName("HTTPResponse").Interface().(*http.Response)
	body, _ := v.FieldByName("Body").Interface().([]byte)
	if err := Check(nil, hr, body); err != nil {
		return r, err
	}
	return r, nil
}

// retryTransport retries rate-limited requests (429) and requests refused
// because another action is still running on the resource (409 resource_busy).
type retryTransport struct {
	next       http.RoundTripper
	maxRetries int
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := t.next.RoundTrip(req)
		if err != nil || attempt >= t.maxRetries {
			return resp, err
		}
		wait := time.Duration(0)
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			wait = RetryAfter(resp.Header.Get("Retry-After"), time.Now())
		case http.StatusConflict:
			body, rerr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if rerr != nil {
				return nil, rerr
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			var env ErrorResponse
			if json.Unmarshal(body, &env) == nil && (env.Error.Code == "resource_busy" || env.Error.Code == "locked") {
				wait = time.Duration(attempt+1) * time.Second
			}
		}
		if wait == 0 {
			return resp, nil
		}
		if req.Body != nil {
			if req.GetBody == nil {
				return resp, nil
			}
			b, err := req.GetBody()
			if err != nil {
				return resp, nil
			}
			req.Body = b
		}
		_ = resp.Body.Close()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(wait):
		}
	}
}

// ID helpers -----------------------------------------------------------------

// ParseID parses a numeric resource ID.
func ParseID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid ID %q: must be a positive integer", s)
	}
	return id, nil
}

// FormatID formats a numeric resource ID.
func FormatID(id int64) string { return strconv.FormatInt(id, 10) }

// maxRetryAfter caps how long a single 429 makes the provider sleep.
const maxRetryAfter = 10 * time.Minute

// RetryAfter parses a Retry-After header (seconds or HTTP date). It falls back
// to one second when the header is missing or invalid and caps the wait at
// ten minutes.
func RetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	d := time.Second
	if n, err := strconv.Atoi(h); err == nil && n > 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(h); err == nil && t.After(now) {
		d = t.Sub(now)
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}
