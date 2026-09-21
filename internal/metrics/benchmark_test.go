package metrics

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func BenchmarkParse10000Series(b *testing.B) {
	var body strings.Builder
	body.WriteString("# HELP queue_depth Pending work items\n# TYPE queue_depth gauge\n")
	for i := range 10000 {
		fmt.Fprintf(&body, "queue_depth{pool=\"pool-%05d\",zone=\"az-a\"} %d\n", i, i)
	}
	data := []byte(body.String())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := Parse(data, "text/plain", 10000); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHistogram1000Groups(b *testing.B) {
	makeSnapshot := func(mult int) Snapshot {
		var body strings.Builder
		body.WriteString("# TYPE latency histogram\n")
		for i := range 1000 {
			fmt.Fprintf(&body, "latency_bucket{route=\"%d\",le=\"1\"} %d\nlatency_bucket{route=\"%d\",le=\"2\"} %d\nlatency_bucket{route=\"%d\",le=\"+Inf\"} %d\nlatency_count{route=\"%d\"} %d\nlatency_sum{route=\"%d\"} %d\n", i, 50*mult, i, 90*mult, i, 100*mult, i, 100*mult, i, 150*mult)
		}
		s, err := Parse([]byte(body.String()), "text/plain", 10000)
		if err != nil {
			b.Fatal(err)
		}
		s.At = time.Unix(int64(mult*5), 0)
		return s
	}
	previous, current := makeSnapshot(1), makeSnapshot(2)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if results := Evaluate(current, previous, Selection{Metric: "latency", View: "p95"}); len(results) != 1000 {
			b.Fatal("lost histogram groups")
		}
	}
}
