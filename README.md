# termfana

English | [简体中文](README.zh-CN.md)

[![CI](https://github.com/laixintao/termfana/actions/workflows/ci.yml/badge.svg)](https://github.com/laixintao/termfana/actions/workflows/ci.yml)

Explore metrics in your terminal. Connect directly to an application's `/metrics` endpoint to inspect trends, throughput, and latency while debugging over SSH.

One binary and one metrics URL are all you need. termfana scrapes the endpoint at a configurable interval and keeps recent history in memory.

## Quick start

Building requires Go 1.26 or later. The compiled binary runs without a Go installation.

Download the `.tar.gz` for your platform from [GitHub Releases](https://github.com/laixintao/termfana/releases), extract it, and run `./termfana`. Releases include Linux/macOS binaries for amd64/arm64 and a `SHA256SUMS` file. Verify with `sha256sum --check SHA256SUMS` on Linux or `shasum -a 256 --check SHA256SUMS` on macOS; checking the entire manifest requires all four archives.

You can also install with Go: `go install github.com/laixintao/termfana/cmd/termfana@latest`.

To build from source:

```sh
make build

# Try synthetic traffic, latency spikes, and counter resets
./bin/termfana demo

# Connect to an application's full /metrics URL
./bin/termfana http://localhost:8080/metrics

# Scrape faster, keep 600 rounds, and start with a 2-minute window
./bin/termfana --interval 1s --capacity 600 --window 2m \
  http://localhost:8080/metrics
```

Search for a metric and press Enter to add a panel. Press `a` to add related metrics, with up to four panels open at once. Collection starts immediately, so metrics added later can still use the retained history.

Wide terminals show panels in two columns. At 80×24, the focused panel fills the workspace; use Tab or a number key to switch panels. Use `--ascii` if your terminal font does not render Braille charts well.

## Debugging controls

| Key | Action |
| --- | --- |
| `a` / `/` | Open the metric browser / search |
| `Enter` | Add a metric; maximize the focused panel in the dashboard |
| `Tab` / `Shift+Tab` / `1`–`4` | Switch panels |
| `v` | Switch between raw values, rates, and histogram views |
| `l` | Filter labels: Enter to toggle, `c` to clear, Esc to return |
| `g` | Inspect series; Enter for labels and cursor values, Space to hide/show |
| `←` / `→` | Inspect a sample with a cursor shared across all panels |
| `+` / `-` | Zoom the time window |
| `[` / `]` | Pan backward / forward in time |
| `Space` | Freeze the view / return to live mode; collection continues |
| `r` / `Home` | Return to live mode |
| `d` | Remove the focused panel |
| `s` | Save session configuration; replaces the file if it already exists |
| `i` | Show the metric's full description in the browser |
| `?` | Show keyboard help |
| `q` / `Ctrl+C` | Exit and restore the terminal |

Each panel plots at most eight series and shows the displayed count in its footer. Filter labels or hide series to choose what to compare; press `g` for exact values and complete labels. Defaults are a 5-second interval and 360 retained rounds, or about 30 minutes. Changing the interval changes how much time those 360 rounds cover.

## Metric calculations

| Type | Views |
| --- | --- |
| Gauge / no declared TYPE | Raw values |
| Counter | `rate` by default: delta between consecutive samples divided by elapsed seconds; `raw` is also available |
| Classic histogram | `p95` by default; also `p50`, `p99`, `mean`, `rate`, and `raw` |
| Summary | Exposed quantile, sum, and count values |

- Histogram quantiles use differences between consecutive cumulative bucket counts and linear interpolation within each bucket. Charts mark estimates with `≈`. `mean` is `Δsum / Δcount`; `rate` is `Δcount / Δtime`.
- Histograms are grouped by every label except `le`. Summary quantiles are displayed as exposed, without aggregation or recalculation.
- Derived views establish a new baseline on the first scrape, counter decreases, creation timestamp changes, failed or missing scrapes, and series reappearance. Histograms with no new observations show `N/A` for quantiles and mean, and zero for request rate.
- Failed, missing, `NaN`, `Inf`, and otherwise uncomputable samples remain gaps. Charts do not replace gaps with zero or join lines across them.
- All series use the local scrape completion time. Timestamps exposed by the endpoint are not used for the time axis or rate calculations.
- Resets cannot always be detected between scrapes: if a counter has no creation timestamp and already exceeds its previous value after a reset, the reset is indistinguishable from normal growth.

Prometheus text and OpenMetrics 1.0 are supported, including HELP, TYPE, UNIT, and escaped labels. Exemplars are parsed but not displayed. This version supports classic histograms; native histograms, PromQL, and aggregation across series are not implemented.

## Scripts and pipelines

Place options before the URL.

```sh
# List available metrics and types
./bin/termfana list http://localhost:8080/metrics

# Emit the metric catalog as one JSON array
./bin/termfana list --format json http://localhost:8080/metrics

# Read one round of raw values
./bin/termfana sample --metric process_resident_memory_bytes \
  http://localhost:8080/metrics

# Emit one JSON object per round, suitable for jq or a file
./bin/termfana sample --metric http_requests_total \
  --view rate --label method=GET --label status=500 \
  --interval 1s --count 12 --format json \
  http://localhost:8080/metrics | jq .

# Read histogram p95 using the full family name
./bin/termfana sample --metric http_request_duration_seconds \
  --view p95 --count 6 --format json http://localhost:8080/metrics

# Stream until Ctrl+C
./bin/termfana sample --metric workers --count 0 --format json \
  http://localhost:8080/metrics > workers.jsonl
```

`sample` defaults to `raw` and one output round. Derived views take one baseline scrape before emitting the number of rounds requested by `--count`. Scrapes follow `--interval`. Label filters use exact matching; multiple filters are combined with AND.

Example JSON Lines record:

```json
{"timestamp":"2026-09-21T14:00:05+08:00","duration_ms":2.4,"view":"rate","status":"ok","samples":[{"metric":"http_requests_total","labels":{"method":"GET","status":"500"},"value":1.2,"status":"ok"}]}
```

Unavailable values are `null`. Each sample's `status` explains why, such as `warming_up`, `reset`, `non_finite`, or `no_observations`. A failed scrape emits a record with `status: "scrape_failed"`, an empty `samples` array, and an `error` message; collection continues on the next round. No matching series produces `no_matches`.

Data goes to stdout; diagnostics go to stderr. Exit codes are `0` for success, `1` for a collection or output failure, and `2` for invalid arguments or configuration. A streaming command returns `1` if any scrape failed during the run. Interactive mode requires a TTY; use `list` or `sample` with redirection and pipes.

## Sessions and connections

Press `s` to save panels, label filters, views, the endpoint, and scrape settings. Restore with:

```sh
./bin/termfana --session debug.json
```

Sessions are versioned JSON containing configuration only; collected history stays in memory. Saves replace the file atomically and set permissions to `0600`. Explicit command-line options override session settings.

An unauthenticated endpoint only needs a URL. Bearer tokens and Basic authentication use environment variables. Session files store the variable names only.

```sh
# Use an existing APP_METRICS_TOKEN environment variable
./bin/termfana --token-env APP_METRICS_TOKEN https://service.example/metrics

# Use existing APP_METRICS_USER / APP_METRICS_PASSWORD variables
./bin/termfana --username-env APP_METRICS_USER \
  --password-env APP_METRICS_PASSWORD https://service.example/metrics
```

HTTPS uses the system trust store. An existing SSH port forward can be accessed through its local URL. termfana does not perform login flows or create SSH tunnels.

The default request timeout is 3 seconds. Scrapes do not overlap, and failures are retried on the next cycle. Each response is limited to 16 MiB after decompression and 10,000 series; adjust with `--max-bytes` and `--max-series`. Exceeding a limit fails the entire scrape to avoid presenting partial data as complete. The retained series catalog uses the same series limit; rapid label churn can evict older history early, with a notice in the UI.

## Development and verification

```sh
make build           # bin/termfana
make check           # race tests, go vet, and version consistency
make smoke           # Python PTY end-to-end test on Linux/macOS
make dist            # Standalone binaries for Linux/macOS × amd64/arm64
make package         # Four versioned tar.gz archives + SHA256SUMS
```

Tests cover format parsing, HTTP authentication and timeouts, counter resets, histogram interval calculations, bounded history, CLI JSON, terminal sizes, and keyboard interactions. `make smoke` starts a temporary localhost service and exercises four panels, label filters, failure recovery, session save/load, resize, and terminal restoration after normal exit, SIGINT, and SIGTERM. The smoke script uses Python's standard library; installing `pyte` also enables assertions against the rendered screen, as used in CI.

To provide a demo endpoint for other commands:

```sh
./bin/termfana demo --serve
# Prints a temporary loopback /metrics URL; Ctrl+C stops it
```

Code is organized into `metrics` (collection, parsing, history, calculations), `chart` (terminal plotting), `tui`, `cli`, `config`, and `demo`. The TUI and CLI share the collection and calculation core. No Prometheus server is required at runtime.

## Versioning and automatic releases

Maintainers install [bump2version](https://github.com/c4urself/bump2version) once to get the `bumpversion` command:

```sh
pipx install bump2version==1.0.1
```

Commit your changes, then run from a branch with a clean working tree:

```sh
make release PART=patch       # For example, 0.1.0 → 0.1.1; patch is the default
# Or: make release PART=minor # 0.1.0 → 0.2.0
# Or: make release VERSION=0.2.0
```

This calls bumpversion to update `.bumpversion.cfg` and the CLI version, creates a version commit and annotated `vX.Y.Z` tag, and atomically pushes the current branch and that tag to `origin`. If the push fails, the local commit and tag remain. Resolve the Git error and retry the push printed by the command; do not bump again.

You can also run the steps separately:

```sh
bumpversion patch
git push --atomic origin HEAD --follow-tags
```

A local bump does not contact GitHub; pushing the tag triggers the release. Branch pushes and pull requests run race tests, `go vet`, version checks, release automation tests, and real PTY tests on Linux and macOS. Version tags run the same tests. After every test passes, the release workflow builds Linux/macOS × amd64/arm64 archives and publishes a GitHub Release with checksums and generated release notes. No additional secrets are required; publishing uses the repository's `GITHUB_TOKEN`.

A mismatch between the configured version, CLI version, and tag blocks publication. Stable `major.minor.patch` versions are supported. Editing the source version alone does not publish a release; the matching tag must be pushed.

Run the [Release workflow](https://github.com/laixintao/termfana/actions/workflows/release.yml) manually on a branch to test packaging. It produces a `release-assets` artifact without publishing a GitHub Release. Changes to the release workflow or packaging script on `master` also trigger this check. A manual run on a version tag publishes that version.

To test release automation locally, using disposable repositories only:

```sh
python3 -m pip install -r scripts/requirements-ci.txt
make release-test
```
