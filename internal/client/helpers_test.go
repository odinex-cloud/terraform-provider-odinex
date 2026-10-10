package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Odinex {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{Endpoint: srv.URL, Token: "odx_test", PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAuthAndErrorEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer odx_test" {
			t.Errorf("missing token: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"Server not found"}}`)
	})
	_, err := Result(c.GetServerWithResponse(context.Background(), 7))
	if !IsNotFound(err) {
		t.Fatalf("want not found, got %v", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "not_found" || ae.Message != "Server not found" || ae.URL != "/servers/7" {
		t.Errorf("decoded error: %+v", ae)
	}
}

func TestWaitForActionError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		a := Action{Id: 9, Command: "create_server", Status: "running"}
		if n >= 2 {
			a.Status = "error"
			a.Error = &ActionError{Code: "no_capacity", Message: "No capacity in fra1"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"action": a})
	})
	err := c.WaitForAction(context.Background(), &Action{Id: 9, Command: "create_server", Status: "running"})
	var af *ActionFailedError
	if !errors.As(err, &af) || af.Action.Error.Code != "no_capacity" {
		t.Fatalf("want action error, got %v", err)
	}
	if got := err.Error(); got != "action 9 (create_server) failed: no_capacity: No capacity in fra1" {
		t.Errorf("message: %s", got)
	}
	if err := c.WaitForAction(context.Background(), &Action{Status: "success"}); err != nil {
		t.Errorf("finished action: %v", err)
	}
	if err := c.WaitForAction(context.Background(), nil); err != nil {
		t.Errorf("nil action: %v", err)
	}
}

func TestRetryOn429AndBusy(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) == "" {
			t.Error("request body lost on retry")
		}
		w.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`)
		case 2:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"code":"resource_busy","message":"busy"}}`)
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"action":{"id":1,"command":"poweron","status":"success","progress":100,"started":"2026-01-01T00:00:00Z","resources":[]}}`)
		}
	})
	res, err := Result(c.ServerRebuildWithResponse(context.Background(), 1, ServerRebuildJSONRequestBody{Image: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || res.JSON201.Action.Id != 1 {
		t.Errorf("calls %d", calls.Load())
	}
}

func TestNoRetryOnOtherConflicts(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"uniqueness_error","message":"name taken"}}`)
	})
	_, err := Result(c.GetServerWithResponse(context.Background(), 1))
	if !IsCode(err, "uniqueness_error") || calls.Load() != 1 {
		t.Errorf("err %v calls %d", err, calls.Load())
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              time.Second,
		"7":                             7 * time.Second,
		"junk":                          time.Second,
		"100000":                        maxRetryAfter,
		"Thu, 01 Jan 2026 00:00:30 GMT": 30 * time.Second,
	}
	for h, want := range cases {
		if got := RetryAfter(h, now); got != want {
			t.Errorf("RetryAfter(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestParseID(t *testing.T) {
	if id, err := ParseID("42"); err != nil || id != 42 {
		t.Error(id, err)
	}
	for _, bad := range []string{"", "0", "-1", "x"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) should fail", bad)
		}
	}
}
