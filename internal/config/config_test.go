package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"termfana/internal/metrics"
)

func TestSessionRoundTrip(t *testing.T) {
	c := Default()
	c.Connection.URL = "http://localhost:9090/metrics"
	c.Connection.TokenEnv = "TEST_SECRET"
	t.Setenv("TEST_SECRET", "must-not-be-saved")
	c.Panels = []metrics.Selection{{Metric: "requests_total", Labels: metrics.Labels{"method": "GET"}, View: "rate", Hidden: []string{"a"}}}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip: %+v", got)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "must-not-be-saved") {
		t.Fatal("credential leaked")
	}
	stat, _ := os.Stat(path)
	if stat.Mode().Perm() != 0600 {
		t.Fatalf("mode %v", stat.Mode())
	}
	c.Panels = nil
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(path); err != nil || len(got.Panels) != 0 {
		t.Fatal("overwrite did not replace session")
	}
}

func TestRejectInvalidSession(t *testing.T) {
	for _, body := range []string{`{"version":2}`, `{"wat":1}`, `{"connection":{"url":"file:///tmp/test"}}`, `{"connection":{"url":"https://user:pass@host/metrics"}}`, `{"connection":{"url":"http://localhost/metrics"},"capacity":0}`, `{"connection":{"url":"http://localhost/metrics"}} {}`} {
		path := filepath.Join(t.TempDir(), "bad.json")
		os.WriteFile(path, []byte(body), 0600)
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
