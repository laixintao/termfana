package metrics

import (
	"compress/gzip"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func snapshot(t *testing.T, body string, second int) Snapshot {
	t.Helper()
	s, err := Parse([]byte(body), "text/plain; version=0.0.4", 10000)
	if err != nil {
		t.Fatal(err)
	}
	s.At = time.Unix(int64(second), 0)
	return s
}

func counter(t *testing.T, value float64, second int) Snapshot {
	return snapshot(t, fmt.Sprintf("# HELP requests_total Completed requests\n# TYPE requests_total counter\nrequests_total{method=\"GET\",status=\"200\"} %g\n", value), second)
}

func TestParseFormats(t *testing.T) {
	body := "# HELP temperature Temperature\\nreading\n# TYPE temperature gauge\ntemperature{room=\"a\\\"b\",path=\"C:\\\\temp\"} -4\nunknown NaN\nother +Inf\n"
	s := snapshot(t, body, 0)
	if s.Families["temperature"].Help != "Temperature\nreading" || len(s.Values) != 3 {
		t.Fatalf("unexpected snapshot: %+v", s)
	}
	for key, d := range s.Definitions {
		if d.Name == "temperature" && (d.Labels["room"] != "a\"b" || s.Values[key].Value != -4) {
			t.Fatalf("bad escaping: %+v", d)
		}
	}
	om := "# HELP requests Requests\n# TYPE requests counter\nrequests_total{method=\"GET\"} 12 # {trace_id=\"abc\"} 1 1700000000\nrequests_created{method=\"GET\"} 1699999000\n# TYPE duration_seconds histogram\n# UNIT duration_seconds seconds\nduration_seconds_bucket{le=\"1\"} 2\nduration_seconds_bucket{le=\"+Inf\"} 3\nduration_seconds_count 3\nduration_seconds_sum 2.5\n# EOF\n"
	o, err := Parse([]byte(om), "application/openmetrics-text; version=1.0.0", 100)
	if err != nil {
		t.Fatal(err)
	}
	if o.Families["requests"].Type != "counter" || o.Families["duration_seconds"].Unit != "seconds" {
		t.Fatalf("bad OpenMetrics metadata: %+v", o.Families)
	}
	for key, d := range o.Definitions {
		if d.Name == "requests_total" && (d.Family != "requests" || o.Values[key].Created != 1699999000000) {
			t.Fatalf("bad counter family/creation: %+v %+v", d, o.Values[key])
		}
	}
}

func TestRejectMalformedAndOversizedScrapes(t *testing.T) {
	for _, tc := range []struct {
		body, typ string
		limit     int
	}{
		{"a 1\na 2\n", "text/plain", 10},
		{"a 1\nb 2\n", "text/plain", 1},
		{"a 1\ninvalid{\n", "text/plain", 10},
		{"<html>error</html>", "text/html", 10},
		{"a 1\n# EOF\n", "application/openmetrics-text;version=2.0.0", 10},
		{"a 1\n", "application/openmetrics-text;version=1.0.0", 10},
	} {
		if s, err := Parse([]byte(tc.body), tc.typ, tc.limit); err == nil || len(s.Values) > 0 {
			t.Fatalf("accepted malformed/oversized scrape: %q %v", tc.body, err)
		}
	}
}

func TestQuotedUTF8NamesHaveDistinctIdentities(t *testing.T) {
	a := Labels{"a": "x", "b": "y"}
	b := Labels{`a="x",b`: "y"}
	if a.String() == b.String() {
		t.Fatal("quoted label names collided with separate labels")
	}
	body := "custom{a=\"x\",b=\"y\"} 1\ncustom{\"a=\\\"x\\\",b\"=\"y\"} 2\n{\"metric.with.dots\",\"http.status\"=\"200\"} 3\n"
	s := snapshot(t, body, 0)
	if len(s.Values) != 3 {
		t.Fatal("lost distinct UTF-8 series")
	}
}

func TestScraperHTTP(t *testing.T) {
	t.Setenv("TERMFANA_TEST_TOKEN", "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || !strings.Contains(r.Header.Get("Accept"), "openmetrics") {
			t.Error("missing auth/negotiation")
		}
		w.Header().Set("Content-Type", "text/plain;version=0.0.4")
		w.Header().Set("Content-Encoding", "gzip")
		g := gzip.NewWriter(w)
		fmt.Fprint(g, "a 1\n")
		g.Close()
	}))
	defer server.Close()
	s, err := NewScraper(Connection{URL: server.URL, TokenEnv: "TERMFANA_TEST_TOKEN"}, time.Second, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Scrape(context.Background())
	if got.Error != "" || len(got.Values) != 1 || got.At.IsZero() || got.Duration <= 0 {
		t.Fatalf("scrape: %+v", got)
	}
	s.MaxBytes = 2
	got = s.Scrape(context.Background())
	if !strings.Contains(got.Error, "exceeds") || len(got.Values) > 0 {
		t.Fatalf("body limit: %+v", got)
	}
}

func TestScraperFailureAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s, _ := NewScraper(Connection{URL: server.URL + "?token=secret"}, time.Second, 100, 10)
	got := s.Scrape(context.Background())
	if !strings.Contains(got.Error, "503") || strings.Contains(got.Error, "secret") {
		t.Fatal(got.Error)
	}
	s.Connection.URL = server.URL + "/slow?token=secret"
	s.Client.Timeout = 10 * time.Millisecond
	got = s.Scrape(context.Background())
	if got.Error == "" || strings.Contains(got.Error, "secret") || len(got.Values) != 0 {
		t.Fatalf("timeout: %+v", got)
	}
}

func TestBasicAuthAndCrossOriginRedirect(t *testing.T) {
	t.Setenv("TEST_USER", "alice")
	t.Setenv("TEST_PASS", "password")
	received := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "password" {
			t.Error("bad Basic auth")
		}
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer server.Close()
	s, err := NewScraper(Connection{URL: server.URL, UsernameEnv: "TEST_USER", PasswordEnv: "TEST_PASS"}, time.Second, 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Scrape(context.Background()); got.Error == "" || received {
		t.Fatal("redirect must not forward credentials")
	}
}

func TestCounterRatesAndGaps(t *testing.T) {
	sel := Selection{Metric: "requests_total", View: "rate"}
	for _, tc := range []struct {
		name        string
		now, before Snapshot
		want        *float64
		status      string
	}{
		{"actual elapsed", counter(t, 35, 15), counter(t, 10, 5), ptr(2.5), "ok"},
		{"unchanged", counter(t, 10, 10), counter(t, 10, 5), ptr(0), "ok"},
		{"first", counter(t, 10, 10), Snapshot{}, nil, "warming_up"},
		{"reset", counter(t, 2, 10), counter(t, 10, 5), nil, "reset"},
		{"failure", counter(t, 12, 10), Snapshot{Error: "timeout", At: time.Unix(5, 0)}, nil, "warming_up"},
		{"nonfinite", counter(t, math.NaN(), 10), counter(t, 10, 5), nil, "non_finite"},
		{"clock", counter(t, 12, 5), counter(t, 10, 5), nil, "invalid_interval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.now, tc.before, sel)
			if len(got) != 1 || got[0].Status != tc.status {
				t.Fatalf("%+v", got)
			}
			if tc.want != nil {
				if got[0].Value == nil || math.Abs(*got[0].Value-*tc.want) > 1e-9 {
					t.Fatalf("bad value: %+v", got)
				}
			} else if got[0].Value != nil {
				t.Fatal("gap must not be zero")
			}
		})
	}
	a, b := counter(t, 10, 5), counter(t, 20, 10)
	for key, value := range b.Values {
		value.Created = 1000
		b.Values[key] = value
	}
	if got := Evaluate(b, a, sel); got[0].Status != "reset" {
		t.Fatalf("creation change: %+v", got)
	}
	if got := Evaluate(b, a, Selection{Metric: "requests_total", View: "raw", Labels: Labels{"missing": ""}}); len(got) != 0 {
		t.Fatal("missing labels must not equal empty labels")
	}
}

func ptr(v float64) *float64 { return &v }

func histogramSnapshot(t *testing.T, mult int, second int) Snapshot {
	return snapshot(t, fmt.Sprintf("# TYPE latency histogram\nlatency_bucket{route=\"/\",le=\"1\"} %d\nlatency_bucket{route=\"/\",le=\"2\"} %d\nlatency_bucket{route=\"/\",le=\"+Inf\"} %d\nlatency_count{route=\"/\"} %d\nlatency_sum{route=\"/\"} %d\n", 50*mult, 90*mult, 100*mult, 100*mult, 150*mult), second)
}

func TestHistogramIntervalStatistics(t *testing.T) {
	a, b := histogramSnapshot(t, 1, 5), histogramSnapshot(t, 2, 15)
	for view, want := range map[string]float64{"rate": 10, "mean": 1.5, "p50": 1, "p95": 2, "p99": 2} {
		got := Evaluate(b, a, Selection{Metric: "latency", View: view})
		if len(got) != 1 || got[0].Value == nil || math.Abs(*got[0].Value-want) > 1e-9 {
			t.Fatalf("%s: %+v", view, got)
		}
	}
	for _, tc := range []struct {
		name      string
		now, prev Snapshot
		status    string
	}{
		{"reset", a, b, "invalid_interval"},
		{"reset forward", histogramSnapshot(t, 0, 20), b, "reset"},
		{"zero observations", histogramSnapshot(t, 2, 20), b, "no_observations"},
		{"warming up", b, Snapshot{}, "warming_up"},
	} {
		got := Evaluate(tc.now, tc.prev, Selection{Metric: "latency", View: "p95"})
		if len(got) != 1 || got[0].Status != tc.status {
			t.Fatalf("%s: %+v", tc.name, got)
		}
	}
	for key, d := range a.Definitions {
		if d.Name == "latency_bucket" && (d.Labels["le"] == "1" || d.Labels["le"] == "1.0") {
			delete(a.Values, key)
		}
	}
	if got := Evaluate(b, a, Selection{Metric: "latency", View: "p95"}); got[0].Value != nil {
		t.Fatal("changed buckets must produce a gap")
	}
}

func TestSummaryAndUntypedRemainRaw(t *testing.T) {
	s := snapshot(t, "# TYPE latency summary\nlatency{quantile=\"0.95\"} 0.7\nlatency_sum 10\nlatency_count 20\nno_type_total 12\n", 1)
	if DefaultView(s.Families["no_type_total"]) != "raw" {
		t.Fatal("must not infer type from metric suffix")
	}
	got := Evaluate(s, Snapshot{}, Selection{Metric: "latency", View: "raw"})
	if len(got) != 3 {
		t.Fatalf("summary raw: %+v", got)
	}
	if err := ValidateSelection(s, Selection{Metric: "latency", View: "p95"}); err == nil {
		t.Fatal("must not recompute summary quantiles")
	}
}

func TestHistoryBoundsAndChurn(t *testing.T) {
	h := NewHistory(5, 4)
	for n := 0; n < 500; n++ {
		s := snapshot(t, fmt.Sprintf("# TYPE gauge gauge\ngauge{id=\"%d\"} %d\n", n, n), n)
		h.Append(s)
		if h.Len() > 5 || len(h.definitions) > 4 || len(h.refs) > 4 {
			t.Fatalf("history grew: frames=%d definitions=%d", h.Len(), len(h.definitions))
		}
		frames := h.Snapshots()
		if frames[len(frames)-1].At != s.At {
			t.Fatal("wrong ring order")
		}
	}
	h.Append(Snapshot{At: time.Unix(501, 0), Error: "failed"})
	if h.Snapshots()[h.Len()-1].Error == "" {
		t.Fatal("scrape failures must remain in the timeline")
	}
}
