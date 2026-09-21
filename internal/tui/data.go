package tui

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/laixintao/termfana/internal/chart"
	"github.com/laixintao/termfana/internal/metrics"
)

type panelData struct {
	signature string
	series    []chart.Series
	available []metrics.Result
	results   map[string][]metrics.Result
	total     int
}

func buildPanel(frames []metrics.Snapshot, sel metrics.Selection) panelData {
	d := panelData{results: map[string][]metrics.Result{}}
	if len(frames) == 0 {
		return d
	}
	definition := frames[len(frames)-1].Definitions
	identities := map[string]metrics.Result{}
	inputToOutput := map[string]string{}
	histogram := sel.View != "raw" && sel.View != "rate"
	for i := len(frames) - 1; i >= 0; i-- {
		if f, ok := metrics.ResolveFamily(frames[i], sel.Metric); ok {
			histogram = f.Type == "histogram" && sel.View != "raw"
			break
		}
	}
	for key, def := range definition {
		if (def.Name != sel.Metric && def.Family != sel.Metric) || !def.Labels.Matches(sel.Labels) {
			continue
		}
		if sel.View != "raw" && strings.HasSuffix(def.Name, "_created") {
			continue
		}
		name, labels := def.Name, def.Labels
		if histogram {
			name = def.Family
			labels = metrics.Labels{}
			for k, v := range def.Labels {
				if k != "le" {
					labels[k] = v
				}
			}
		}
		r := metrics.Result{Metric: name, Labels: labels, Status: "missing"}
		identities[r.Key()] = r
		inputToOutput[key] = r.Key()
	}
	keys := make([]string, 0, len(identities))
	for key := range identities {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hidden := map[string]bool{}
	for _, key := range sel.Hidden {
		hidden[key] = true
	}
	selected := map[string]int{}
	for _, key := range keys {
		d.available = append(d.available, identities[key])
		if hidden[key] || len(d.series) >= 8 {
			continue
		}
		selected[key] = len(d.series)
		d.series = append(d.series, chart.Series{Key: key, Label: identities[key].Labels.String()})
	}
	d.total = len(keys)
	inputKeys := []string{}
	for input, output := range inputToOutput {
		if _, ok := selected[output]; ok {
			inputKeys = append(inputKeys, input)
		}
	}
	var previous metrics.Snapshot
	for _, frame := range frames {
		s := frame
		s.Values = make(map[string]metrics.Sample, len(inputKeys))
		for _, key := range inputKeys {
			if v, ok := frame.Values[key]; ok {
				s.Values[key] = v
			}
		}
		results := metrics.Evaluate(s, previous, sel)
		current := map[string]metrics.Result{}
		for _, r := range results {
			current[r.Key()] = r
		}
		for i := range d.series {
			key := d.series[i].Key
			r, ok := current[key]
			if !ok {
				r = identities[key]
				if frame.Error != "" {
					r.Status = "scrape_failed"
				}
			}
			d.series[i].Points = append(d.series[i].Points, chart.Point{At: frame.At, Value: r.Value})
			d.results[key] = append(d.results[key], r)
		}
		previous = s
	}
	return d
}

func selectionSignature(sel metrics.Selection) string { b, _ := json.Marshal(sel); return string(b) }
