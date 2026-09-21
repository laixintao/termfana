package demo

import (
	"fmt"
	"math"
	"net"
	"net/http"
	"sync"
	"time"
)

// Start exposes synthetic metrics on loopback. The ordinary HTTP scraper reads
// this endpoint; demo values never masquerade as measurements of this machine.
func Start() (string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	start, last := time.Now(), time.Now()
	var mu sync.Mutex
	var requests, failures, sum float64
	buckets := []float64{0, 0, 0, 0, 0}
	cycle := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		t := now.Sub(start).Seconds()
		dt := now.Sub(last).Seconds()
		last = now
		if int(t/90) != cycle {
			cycle = int(t / 90)
			requests, failures, sum = 0, 0, 0
			for i := range buckets {
				buckets[i] = 0
			}
		}
		rate := 90 + 35*math.Sin(t/8)
		spike := math.Pow(math.Max(0, math.Sin(t/12)), 10)
		n := math.Max(1, math.Round(rate*dt))
		requests += n
		failures += math.Round(n * (.005 + .12*spike))
		fractions := []float64{.35 - .3*spike, .8 - .6*spike, .96 - .35*spike, .998 - .1*spike, 1}
		for i, fraction := range fractions {
			buckets[i] += math.Round(n * fraction)
		}
		sum += n * (.07 + .35*spike)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, "# HELP demo_requests_total Synthetic completed requests\n# TYPE demo_requests_total counter\n")
		fmt.Fprintf(w, "demo_requests_total{method=\"GET\",status=\"200\"} %.0f\ndemo_requests_total{method=\"GET\",status=\"500\"} %.0f\n", requests-failures, failures)
		fmt.Fprintf(w, "# HELP demo_memory_bytes Synthetic working set\n# TYPE demo_memory_bytes gauge\ndemo_memory_bytes %.0f\n", (110+12*math.Sin(t/15)+18*spike)*1024*1024)
		fmt.Fprintf(w, "# HELP demo_inflight_requests Synthetic in-flight requests\n# TYPE demo_inflight_requests gauge\ndemo_inflight_requests %.0f\n", 12+8*math.Sin(t/5)+55*spike)
		fmt.Fprint(w, "# HELP demo_request_duration_seconds Synthetic request latency\n# TYPE demo_request_duration_seconds histogram\n")
		for i, bound := range []string{"0.05", "0.1", "0.25", "1", "+Inf"} {
			fmt.Fprintf(w, "demo_request_duration_seconds_bucket{le=%q} %.0f\n", bound, buckets[i])
		}
		fmt.Fprintf(w, "demo_request_duration_seconds_sum %f\ndemo_request_duration_seconds_count %.0f\n", sum, requests)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second}
	go server.Serve(l)
	return "http://" + l.Addr().String() + "/metrics", func() { server.Close() }, nil
}
