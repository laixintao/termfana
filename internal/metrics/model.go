package metrics

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Labels map[string]string

func (l Labels) String() string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		name := k
		if !legacyName(k, false) {
			name = strconv.Quote(k)
		}
		parts = append(parts, name+"="+strconv.Quote(l[k]))
	}
	return strings.Join(parts, ",")
}

func legacyName(s string, metric bool) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') || (metric && r == ':') {
			continue
		}
		return false
	}
	return true
}

func seriesKey(name string, labels Labels) string {
	if !legacyName(name, true) {
		name = strconv.Quote(name)
	}
	return name + "{" + labels.String() + "}"
}

func (l Labels) Matches(filter Labels) bool {
	for k, v := range filter {
		actual, ok := l[k]
		if !ok || actual != v {
			return false
		}
	}
	return true
}

type Family struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Help   string `json:"help,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Series int    `json:"series"`
}

type Definition struct {
	Name   string `json:"name"`
	Family string `json:"family"`
	Labels Labels `json:"labels"`
}

func (d Definition) Key() string { return seriesKey(d.Name, d.Labels) }

type Sample struct {
	Value   float64
	Created int64
}

// A snapshot is committed atomically: a failed scrape never contains partial data.
type Snapshot struct {
	At          time.Time
	Duration    time.Duration
	Error       string
	Families    map[string]Family
	Definitions map[string]Definition
	Values      map[string]Sample
}

type Selection struct {
	Metric string   `json:"metric"`
	Labels Labels   `json:"labels,omitempty"`
	View   string   `json:"view"`
	Hidden []string `json:"hidden,omitempty"`
}

type Result struct {
	Metric string   `json:"metric"`
	Labels Labels   `json:"labels"`
	Value  *float64 `json:"value"`
	Status string   `json:"status"`
}

func (r Result) Key() string { return seriesKey(r.Metric, r.Labels) }
func Finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func result(name string, labels Labels, value float64, status string) Result {
	r := Result{Metric: name, Labels: labels, Status: status}
	if status == "ok" && Finite(value) {
		r.Value = &value
	} else if status == "ok" {
		r.Status = "non_finite"
	}
	return r
}

func DefaultView(f Family) string {
	switch f.Type {
	case "counter":
		return "rate"
	case "histogram":
		return "p95"
	default:
		return "raw"
	}
}

func Views(f Family) []string {
	switch f.Type {
	case "counter":
		return []string{"rate", "raw"}
	case "histogram":
		return []string{"p95", "p50", "p99", "mean", "rate", "raw"}
	default:
		return []string{"raw"}
	}
}
