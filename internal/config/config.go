package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/laixintao/termfana/internal/metrics"
)

type Config struct {
	Version    int                 `json:"version"`
	Connection metrics.Connection  `json:"connection"`
	Interval   string              `json:"interval"`
	Timeout    string              `json:"timeout"`
	Capacity   int                 `json:"capacity"`
	MaxSeries  int                 `json:"max_series"`
	MaxBytes   int64               `json:"max_bytes"`
	ASCII      bool                `json:"ascii"`
	Window     string              `json:"window"`
	Panels     []metrics.Selection `json:"panels"`
}

func Default() Config {
	return Config{Version: 1, Interval: "5s", Timeout: "3s", Capacity: 360, MaxSeries: 10000, MaxBytes: 16 << 20, Window: "5m", Panels: []metrics.Selection{}}
}

// Clone gives asynchronous session writes ownership of their maps and slices.
func (c Config) Clone() Config {
	b, _ := json.Marshal(c)
	var copy Config
	_ = json.Unmarshal(b, &copy)
	return copy
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported session version %d", c.Version)
	}
	if err := c.Connection.Validate(); err != nil {
		return err
	}
	for name, value := range map[string]string{"interval": c.Interval, "timeout": c.Timeout, "window": c.Window} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s must be a positive duration", name)
		}
	}
	if c.Capacity < 2 || c.Capacity > 100000 {
		return fmt.Errorf("capacity must be between 2 and 100000")
	}
	if c.MaxSeries < 1 || c.MaxBytes < 1 {
		return fmt.Errorf("max-series and max-bytes must be positive")
	}
	if len(c.Panels) > 4 {
		return fmt.Errorf("at most four panels are supported")
	}
	for _, p := range c.Panels {
		if p.Metric == "" {
			return fmt.Errorf("panel metric must not be empty")
		}
		switch p.View {
		case "", "auto", "raw", "rate", "mean", "p50", "p95", "p99":
		default:
			return fmt.Errorf("unknown panel view %q", p.View)
		}
	}
	return nil
}

func Load(path string) (Config, error) {
	c := Default()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("read session: %w", err)
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return c, fmt.Errorf("session must contain exactly one JSON object")
	}
	return c, c.Validate()
}

func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	f, err := os.CreateTemp(parent, ".termfana-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
