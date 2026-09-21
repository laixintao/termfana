package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/laixintao/termfana/internal/config"
	"github.com/laixintao/termfana/internal/demo"
	"github.com/laixintao/termfana/internal/metrics"
	"github.com/laixintao/termfana/internal/tui"
)

var Version = "0.1.1"

const usage = `termfana — inspect a program's /metrics from your terminal

Usage:
  termfana [options] URL                 Interactive metrics explorer
  termfana list [options] URL            List metric families (one scrape)
  termfana sample [options] URL          Sample one metric or family
  termfana --session FILE                Restore a saved workspace
  termfana demo [--serve]                Synthetic metrics / interactive demo

Common options:
  --interval 5s       Scrape interval       --timeout 3s       Request timeout
  --capacity 360      Retained rounds       --max-series 10000 Series limit
  --max-bytes 16777216 Response byte limit   --ascii            ASCII charts
  --window 5m        Initial chart window   --session FILE     Load session JSON
  --token-env NAME                       Bearer token environment variable
  --username-env NAME --password-env NAME Basic auth environment variables

List/sample options:
  --format table|json  JSON is an array for list, one object per line for sample
  --metric NAME        Required for sample; metric name or histogram family
  --label key=value    Repeat for exact label matching (AND)
  --view raw|rate|mean|p50|p95|p99  Default: raw
  --count N            Output rounds, default 1; 0 streams until interrupted

Example:
  termfana sample --metric http_requests_total --view rate --count 12 \
    --format json http://localhost:8080/metrics

Derived views take one baseline scrape before the first output round.
Put options before the URL. Press ? in the TUI for keyboard help.
`

type labelFlags metrics.Labels

func (l *labelFlags) String() string { return metrics.Labels(*l).String() }
func (l *labelFlags) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("label must be key=value")
	}
	if *l == nil {
		*l = labelFlags{}
	}
	(*l)[k] = v
	return nil
}

type outputFrame struct {
	Timestamp  time.Time        `json:"timestamp"`
	DurationMS float64          `json:"duration_ms"`
	View       string           `json:"view"`
	Status     string           `json:"status"`
	Error      string           `json:"error,omitempty"`
	Samples    []metrics.Result `json:"samples"`
}

func Run(ctx context.Context, args []string, out, errOut io.Writer) int {
	command := "watch"
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			fmt.Fprint(out, usage)
			return 0
		case "version", "--version":
			fmt.Fprintln(out, Version)
			return 0
		case "watch", "list", "sample", "demo":
			command = args[0]
			args = args[1:]
		}
	}
	cfg := config.Default()
	var sessionPath string
	for i, arg := range args {
		if strings.HasPrefix(arg, "--session=") {
			sessionPath = strings.TrimPrefix(arg, "--session=")
		}
		if (arg == "--session" || arg == "-session") && i+1 < len(args) {
			sessionPath = args[i+1]
		}
	}
	if sessionPath != "" {
		var err error
		cfg, err = config.Load(sessionPath)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
	}
	if command == "demo" {
		cfg.Interval, cfg.Window = "1s", "1m"
	}
	fs := flag.NewFlagSet("termfana "+command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() { fmt.Fprint(out, usage) }
	fs.StringVar(&sessionPath, "session", sessionPath, "session JSON file")
	fs.StringVar(&cfg.Interval, "interval", cfg.Interval, "scrape interval")
	fs.StringVar(&cfg.Timeout, "timeout", cfg.Timeout, "request timeout")
	fs.StringVar(&cfg.Window, "window", cfg.Window, "chart window")
	fs.IntVar(&cfg.Capacity, "capacity", cfg.Capacity, "retained rounds")
	fs.IntVar(&cfg.MaxSeries, "max-series", cfg.MaxSeries, "maximum series")
	fs.Int64Var(&cfg.MaxBytes, "max-bytes", cfg.MaxBytes, "maximum decompressed bytes")
	fs.BoolVar(&cfg.ASCII, "ascii", cfg.ASCII, "ASCII chart rendering")
	fs.StringVar(&cfg.Connection.TokenEnv, "token-env", cfg.Connection.TokenEnv, "token environment variable")
	fs.StringVar(&cfg.Connection.UsernameEnv, "username-env", cfg.Connection.UsernameEnv, "username environment variable")
	fs.StringVar(&cfg.Connection.PasswordEnv, "password-env", cfg.Connection.PasswordEnv, "password environment variable")
	format, metric, view := "table", "", "raw"
	count := 1
	serve := false
	filters := labelFlags{}
	fs.StringVar(&format, "format", format, "output format")
	fs.StringVar(&metric, "metric", metric, "metric name")
	fs.StringVar(&view, "view", view, "metric view")
	fs.IntVar(&count, "count", count, "output rounds")
	fs.Var(&filters, "label", "exact label filter")
	fs.BoolVar(&serve, "serve", false, "serve demo metrics without the TUI")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(errOut, "expected one metrics URL; put options before the URL")
		return 2
	}
	if fs.NArg() == 1 {
		cfg.Connection.URL = fs.Arg(0)
	}
	if serve && command != "demo" {
		fmt.Fprintln(errOut, "--serve is only available for demo")
		return 2
	}
	if format != "table" && format != "json" {
		fmt.Fprintln(errOut, "--format must be table or json")
		return 2
	}
	if count < 0 {
		fmt.Fprintln(errOut, "--count must be nonnegative")
		return 2
	}
	if command == "sample" && metric == "" {
		fmt.Fprintln(errOut, "sample requires --metric NAME")
		return 2
	}
	switch view {
	case "raw", "auto", "rate", "mean", "p50", "p95", "p99":
	default:
		fmt.Fprintln(errOut, "unknown --view", view)
		return 2
	}
	if command == "demo" {
		if sessionPath != "" || fs.NArg() != 0 {
			fmt.Fprintln(errOut, "demo uses its own synthetic endpoint; omit URL and --session")
			return 2
		}
		url, stop, err := demo.Start()
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		defer stop()
		cfg.Connection.URL = url
		if serve {
			fmt.Fprintln(out, url)
			<-ctx.Done()
			return 0
		}
		cfg.Panels = []metrics.Selection{{Metric: "demo_requests_total", View: "rate"}, {Metric: "demo_request_duration_seconds", View: "p95"}, {Metric: "demo_memory_bytes", View: "raw"}, {Metric: "demo_inflight_requests", View: "raw"}}
	}
	if cfg.Connection.URL == "" {
		fmt.Fprint(out, usage)
		if len(args) > 0 {
			fmt.Fprintln(errOut, "a metrics URL or --session is required")
			return 2
		}
		return 0
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	timeout, _ := time.ParseDuration(cfg.Timeout)
	scraper, err := metrics.NewScraper(cfg.Connection, timeout, cfg.MaxBytes, cfg.MaxSeries)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	switch command {
	case "list":
		return list(ctx, scraper, format, out, errOut)
	case "sample":
		interval, _ := time.ParseDuration(cfg.Interval)
		return sample(ctx, scraper, metrics.Selection{Metric: metric, Labels: metrics.Labels(filters), View: view}, interval, count, format, out, errOut)
	default:
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			fmt.Fprintln(errOut, "interactive mode requires a terminal; use termfana list or termfana sample for pipes")
			return 2
		}
		if err := tui.Run(ctx, cfg, scraper, command == "demo"); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	}
}

func list(ctx context.Context, scraper *metrics.Scraper, format string, out, errOut io.Writer) int {
	s := scraper.Scrape(ctx)
	if ctx.Err() != nil {
		return 0
	}
	if s.Error != "" {
		fmt.Fprintln(errOut, s.Error)
		return 1
	}
	families := []metrics.Family{}
	for _, f := range s.Families {
		if f.Series > 0 {
			families = append(families, f)
		}
	}
	sort.Slice(families, func(i, j int) bool { return families[i].Name < families[j].Name })
	var err error
	if format == "json" {
		err = json.NewEncoder(out).Encode(families)
	} else {
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "METRIC\tTYPE\tSERIES\tHELP")
		for _, f := range families {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", tui.Safe(f.Name), f.Type, f.Series, tui.Safe(f.Help))
		}
		err = w.Flush()
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func wait(ctx context.Context, duration time.Duration) bool {
	t := time.NewTimer(max(time.Millisecond, duration))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func sample(ctx context.Context, scraper *metrics.Scraper, sel metrics.Selection, interval time.Duration, count int, format string, out, errOut io.Writer) int {
	var previous metrics.Snapshot
	exitCode := 0
	if sel.View != "raw" {
		previous = scraper.Scrape(ctx)
		if ctx.Err() != nil {
			return 0
		}
		if previous.Error != "" {
			fmt.Fprintln(errOut, "baseline:", previous.Error)
			exitCode = 1
		} else if err := metrics.ValidateSelection(previous, sel); err != nil {
			fmt.Fprintln(errOut, err)
			return 2
		}
		if !wait(ctx, interval-previous.Duration) {
			return exitCode
		}
	}
	encoder := json.NewEncoder(out)
	for n := 0; count == 0 || n < count; n++ {
		current := scraper.Scrape(ctx)
		if ctx.Err() != nil {
			return exitCode
		}
		frame := outputFrame{Timestamp: current.At, DurationMS: float64(current.Duration) / float64(time.Millisecond), View: sel.View, Status: "ok", Samples: []metrics.Result{}}
		if current.Error != "" {
			frame.Status, frame.Error = "scrape_failed", current.Error
			exitCode = 1
			fmt.Fprintln(errOut, current.Error)
		} else {
			if err := metrics.ValidateSelection(current, sel); err != nil {
				frame.Status, frame.Error = "unavailable", err.Error()
				exitCode = 1
				fmt.Fprintln(errOut, err)
			} else {
				frame.Samples = metrics.Evaluate(current, previous, sel)
				if len(frame.Samples) == 0 {
					frame.Status = "no_matches"
				}
			}
		}
		var err error
		if format == "json" {
			err = encoder.Encode(frame)
		} else {
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TIME\tMETRIC\tLABELS\tVALUE\tSTATUS")
			if len(frame.Samples) == 0 {
				fmt.Fprintf(w, "%s\t%s\t\tN/A\t%s\n", frame.Timestamp.Format(time.RFC3339), tui.Safe(sel.Metric), frame.Status)
			}
			for _, r := range frame.Samples {
				value := "N/A"
				if r.Value != nil {
					value = fmt.Sprintf("%.10g", *r.Value)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", frame.Timestamp.Format(time.RFC3339), tui.Safe(r.Metric), r.Labels.String(), value, r.Status)
			}
			err = w.Flush()
		}
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		previous = current
		if count > 0 && n+1 == count {
			break
		}
		if !wait(ctx, interval-current.Duration) {
			break
		}
	}
	return exitCode
}
