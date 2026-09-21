package metrics

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/textparse"
)

type Connection struct {
	URL         string `json:"url"`
	TokenEnv    string `json:"token_env,omitempty"`
	UsernameEnv string `json:"username_env,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
}

func (c Connection) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("provide a complete http:// or https:// metrics URL")
	}
	if u.User != nil {
		return fmt.Errorf("URL credentials are not supported; use --username-env and --password-env")
	}
	if u.Fragment != "" {
		return fmt.Errorf("metrics URL must not contain a fragment")
	}
	if c.TokenEnv != "" && (c.UsernameEnv != "" || c.PasswordEnv != "") {
		return fmt.Errorf("choose either Bearer or Basic authentication")
	}
	if (c.UsernameEnv == "") != (c.PasswordEnv == "") {
		return fmt.Errorf("Basic authentication requires both username and password environment variables")
	}
	return nil
}

func (c Connection) DisplayURL() string {
	u, err := url.Parse(c.URL)
	if err != nil {
		return "invalid URL"
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = "redacted"
	}
	return u.String()
}

type Scraper struct {
	Connection Connection
	Client     *http.Client
	MaxBytes   int64
	MaxSeries  int
}

func NewScraper(c Connection, timeout time.Duration, maxBytes int64, maxSeries int) (*Scraper, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if timeout <= 0 || maxBytes <= 0 || maxSeries <= 0 {
		return nil, fmt.Errorf("timeout, max-bytes and max-series must be positive")
	}
	for _, name := range []string{c.TokenEnv, c.UsernameEnv, c.PasswordEnv} {
		if name != "" && os.Getenv(name) == "" {
			return nil, fmt.Errorf("credential environment variable %s is unset or empty", name)
		}
	}
	// Do not send authentication to another origin through a redirect.
	client := &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("cross-origin redirect refused")
		}
		return nil
	}}
	return &Scraper{Connection: c, Client: client, MaxBytes: maxBytes, MaxSeries: maxSeries}, nil
}

func (s *Scraper) Scrape(ctx context.Context) (snap Snapshot) {
	start := time.Now()
	defer func() { snap.At = time.Now(); snap.Duration = time.Since(start) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Connection.URL, nil)
	if err != nil {
		snap.Error = "invalid request"
		return
	}
	req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0;q=1.0,text/plain;version=0.0.4;q=0.9")
	req.Header.Set("User-Agent", "termfana/0.1")
	if s.Connection.TokenEnv != "" {
		req.Header.Set("Authorization", "Bearer "+os.Getenv(s.Connection.TokenEnv))
	}
	if s.Connection.UsernameEnv != "" {
		req.SetBasicAuth(os.Getenv(s.Connection.UsernameEnv), os.Getenv(s.Connection.PasswordEnv))
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		// url.Error includes the potentially sensitive query string.
		if e, ok := err.(*url.Error); ok {
			err = e.Err
		}
		snap.Error = "request failed: " + err.Error()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snap.Error = fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, s.MaxBytes+1))
	if err != nil {
		snap.Error = "read response: " + err.Error()
		return
	}
	if int64(len(body)) > s.MaxBytes {
		snap.Error = fmt.Sprintf("response exceeds %d bytes; increase --max-bytes", s.MaxBytes)
		return
	}
	snap, err = Parse(body, resp.Header.Get("Content-Type"), s.MaxSeries)
	if err != nil {
		snap = Snapshot{Error: err.Error()}
	}
	return
}

func Parse(body []byte, contentType string, maxSeries int) (Snapshot, error) {
	media := "text/plain"
	if contentType != "" {
		var err error
		media, _, err = mime.ParseMediaType(contentType)
		if err != nil {
			return Snapshot{}, fmt.Errorf("invalid Content-Type: %w", err)
		}
	}
	var p textparse.Parser
	switch media {
	case "text/plain":
		p = textparse.NewPromParser(body, labels.NewSymbolTable(), false)
	case "application/openmetrics-text":
		_, params, _ := mime.ParseMediaType(contentType)
		if v := params["version"]; v != "" && v != "1.0.0" {
			return Snapshot{}, fmt.Errorf("unsupported OpenMetrics version %s", v)
		}
		p = textparse.NewOpenMetricsParser(body, labels.NewSymbolTable())
	default:
		return Snapshot{}, fmt.Errorf("unsupported metrics Content-Type %q; use Prometheus text or OpenMetrics 1.0", media)
	}
	s := Snapshot{Families: map[string]Family{}, Definitions: map[string]Definition{}, Values: map[string]Sample{}}
	for {
		entry, err := p.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Snapshot{}, fmt.Errorf("invalid metrics: %w", err)
		}
		switch entry {
		case textparse.EntryType:
			name, typ := p.Type()
			f := s.Families[string(name)]
			f.Name = string(name)
			f.Type = string(typ)
			s.Families[f.Name] = f
		case textparse.EntryHelp:
			name, help := p.Help()
			f := s.Families[string(name)]
			f.Name = string(name)
			f.Help = string(help)
			s.Families[f.Name] = f
		case textparse.EntryUnit:
			name, unit := p.Unit()
			f := s.Families[string(name)]
			f.Name = string(name)
			f.Unit = string(unit)
			s.Families[f.Name] = f
		case textparse.EntryHistogram:
			return Snapshot{}, fmt.Errorf("native histograms are not supported; expose classic text buckets")
		case textparse.EntrySeries:
			_, _, value := p.Series()
			var ls labels.Labels
			p.Labels(&ls)
			d := Definition{Labels: Labels{}}
			ls.Range(func(l labels.Label) {
				if l.Name == "__name__" {
					d.Name = l.Value
				} else {
					d.Labels[l.Name] = l.Value
				}
			})
			key := d.Key()
			if _, ok := s.Values[key]; ok {
				return Snapshot{}, fmt.Errorf("duplicate series %s", key)
			}
			if len(s.Values) >= maxSeries {
				return Snapshot{}, fmt.Errorf("scrape exceeds %d series; increase --max-series", maxSeries)
			}
			s.Definitions[key] = d
			s.Values[key] = Sample{Value: value, Created: p.StartTimestamp()}
		}
	}
	// Resolve families after parsing, including OpenMetrics counter _total names.
	for key, d := range s.Definitions {
		family := d.Name
		if _, exists := s.Families[family]; !exists {
			for _, suffix := range []string{"_bucket", "_count", "_sum", "_total", "_created", "_info", "_gcount", "_gsum"} {
				base := strings.TrimSuffix(d.Name, suffix)
				if base != d.Name {
					if _, ok := s.Families[base]; ok {
						family = base
						break
					}
				}
			}
		}
		d.Family = family
		f := s.Families[family]
		f.Name = family
		if f.Type == "" {
			f.Type = "unknown"
		}
		f.Series++
		s.Families[family] = f
		s.Definitions[key] = d
	}
	// _created is also exposed as an ordinary series by many client libraries.
	for key, d := range s.Definitions {
		if strings.HasSuffix(d.Name, "_created") {
			continue
		}
		createdName := d.Family + "_created"
		if strings.HasSuffix(d.Family, "_total") {
			createdName = strings.TrimSuffix(d.Family, "_total") + "_created"
		}
		baseLabels := Labels{}
		for k, v := range d.Labels {
			if k != "le" && k != "quantile" {
				baseLabels[k] = v
			}
		}
		createdKey := (Definition{Name: createdName, Labels: baseLabels}).Key()
		if c, ok := s.Values[createdKey]; ok && Finite(c.Value) {
			v := s.Values[key]
			v.Created = int64(c.Value * 1000)
			s.Values[key] = v
		}
	}
	return s, nil
}
