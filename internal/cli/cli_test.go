package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListAndSampleJSON(t *testing.T) {
	var n atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "# TYPE requests_total counter\nrequests_total{method=\"GET\"} %d\n# TYPE memory gauge\nmemory 12\n", n.Add(5))
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	if code := Run(context.Background(), []string{"list", "--format", "json", server.URL}, &out, &errs); code != 0 {
		t.Fatalf("list: %d %s", code, &errs)
	}
	var families []map[string]any
	if err := json.Unmarshal(out.Bytes(), &families); err != nil || len(families) != 2 {
		t.Fatalf("JSON: %s", &out)
	}
	out.Reset()
	errs.Reset()
	code := Run(context.Background(), []string{"sample", "--metric", "requests_total", "--label", "method=GET", "--view", "rate", "--count", "3", "--interval", "5ms", "--format", "json", server.URL}, &out, &errs)
	if code != 0 {
		t.Fatalf("sample: %d %s", code, &errs)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 output rounds after baseline: %s", &out)
	}
	for _, line := range lines {
		var frame outputFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		if len(frame.Samples) != 1 || frame.Samples[0].Value == nil || *frame.Samples[0].Value <= 0 {
			t.Fatalf("bad rate %s", line)
		}
	}
}

func TestSampleFailureRecoveryAndNonfinite(t *testing.T) {
	var n atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		if n.Add(1) == 2 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, "value NaN\n")
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	code := Run(context.Background(), []string{"sample", "--metric", "value", "--count", "3", "--interval", "1ms", "--format", "json", server.URL}, &out, &errs)
	if code != 1 || !strings.Contains(errs.String(), "503") {
		t.Fatalf("failure exit: %d %s", code, &errs)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatal(out.String())
	}
	for i, line := range lines {
		var frame outputFrame
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if frame.Status != "scrape_failed" {
				t.Fatal(line)
			}
		} else if frame.Samples[0].Value != nil || frame.Samples[0].Status != "non_finite" {
			t.Fatal(line)
		}
	}
}

func TestUsageErrorsAndCancellation(t *testing.T) {
	for _, args := range [][]string{{"sample"}, {"sample", "--metric", "x", "--view", "bad", "http://localhost/metrics"}, {"--capacity", "1", "http://localhost/metrics"}, {"--token-env", "ABSENT_TERMFANA_TOKEN", "http://localhost/metrics"}, {"bogus"}, {"list", "http://localhost/metrics", "--format", "json"}} {
		var out, errs bytes.Buffer
		if code := Run(context.Background(), args, &out, &errs); code != 2 {
			t.Fatalf("%v: %d %s", args, code, &errs)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if wait(ctx, time.Hour) || time.Since(start) > time.Second {
		t.Fatal("cancellation did not interrupt waiting")
	}
}
