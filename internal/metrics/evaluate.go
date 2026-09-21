package metrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

func ResolveFamily(s Snapshot, metric string) (Family, bool) {
	if f, ok := s.Families[metric]; ok {
		return f, true
	}
	for key := range s.Values {
		d := s.Definitions[key]
		if d.Name == metric {
			return s.Families[d.Family], true
		}
	}
	return Family{}, false
}

func ValidateSelection(s Snapshot, sel Selection) error {
	f, ok := ResolveFamily(s, sel.Metric)
	if !ok {
		return fmt.Errorf("metric %q not found", sel.Metric)
	}
	view := sel.View
	if view == "" || view == "auto" {
		view = DefaultView(f)
	}
	if view == "raw" {
		return nil
	}
	if f.Type == "counter" && view == "rate" && !strings.HasSuffix(sel.Metric, "_created") {
		return nil
	}
	if f.Type == "histogram" && sel.Metric == f.Name {
		if _, ok := sel.Labels["le"]; ok {
			return fmt.Errorf("derived histogram views need every bucket; remove the le filter or use raw")
		}
		for _, v := range Views(f) {
			if v == view {
				return nil
			}
		}
	}
	return fmt.Errorf("view %q is unavailable for %s (%s); choose raw or select the histogram family", view, sel.Metric, f.Type)
}

func Evaluate(current, previous Snapshot, sel Selection) []Result {
	if current.Error != "" {
		return nil
	}
	if ValidateSelection(current, sel) != nil {
		return nil
	}
	f, found := ResolveFamily(current, sel.Metric)
	if !found {
		return nil
	}
	view := sel.View
	if view == "" || view == "auto" {
		view = DefaultView(f)
	}
	if view != "raw" && f.Type == "histogram" {
		return histogram(current, previous, sel, f, view)
	}
	results := []Result{}
	for key, sample := range current.Values {
		d := current.Definitions[key]
		if (d.Name != sel.Metric && d.Family != sel.Metric) || !d.Labels.Matches(sel.Labels) {
			continue
		}
		if view == "rate" && strings.HasSuffix(d.Name, "_created") {
			continue
		}
		status := "ok"
		value := sample.Value
		if view == "rate" {
			before, ok := previous.Values[key]
			status = pairStatus(current, previous, f.Name, sample, before, ok)
			if status == "ok" {
				if value < before.Value {
					status = "reset"
				} else {
					value = (value - before.Value) / current.At.Sub(previous.At).Seconds()
				}
			}
		}
		results = append(results, result(d.Name, d.Labels, value, status))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Key() < results[j].Key() })
	return results
}

func pairStatus(cur, prev Snapshot, family string, now, before Sample, found bool) string {
	if !Finite(now.Value) {
		return "non_finite"
	}
	if prev.Error != "" || !found || prev.At.IsZero() {
		return "warming_up"
	}
	if !Finite(before.Value) {
		return "warming_up"
	}
	if cur.At.Sub(prev.At) <= 0 {
		return "invalid_interval"
	}
	if cur.Families[family].Type != prev.Families[family].Type {
		return "type_changed"
	}
	if now.Created != before.Created {
		return "reset"
	}
	return "ok"
}

type bucket struct{ bound, count float64 }
type histogramGroup struct {
	labels     Labels
	members    map[string]string
	oldBuckets int
}

func histogram(cur, prev Snapshot, sel Selection, f Family, view string) []Result {
	groups := map[string]*histogramGroup{}
	for key := range cur.Values {
		d := cur.Definitions[key]
		if d.Family != f.Name || !d.Labels.Matches(sel.Labels) || strings.HasSuffix(d.Name, "_created") {
			continue
		}
		ls := Labels{}
		for k, v := range d.Labels {
			if k != "le" {
				ls[k] = v
			}
		}
		groupKey := ls.String()
		g := groups[groupKey]
		if g == nil {
			g = &histogramGroup{labels: ls, members: map[string]string{}}
			groups[groupKey] = g
		}
		g.members[d.Name+":"+d.Labels["le"]] = key
	}
	for key := range prev.Values {
		d := prev.Definitions[key]
		if d.Family != f.Name || d.Name != f.Name+"_bucket" {
			continue
		}
		ls := Labels{}
		for k, v := range d.Labels {
			if k != "le" {
				ls[k] = v
			}
		}
		if g := groups[ls.String()]; g != nil {
			g.oldBuckets++
		}
	}
	results := []Result{}
	for _, g := range groups {
		value, status := histogramValue(cur, prev, f.Name, view, g)
		results = append(results, result(f.Name, g.labels, value, status))
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Key() < results[j].Key() })
	return results
}

func histogramValue(cur, prev Snapshot, family, view string, g *histogramGroup) (float64, string) {
	countKey, countOK := g.members[family+"_count:"]
	if !countOK {
		return 0, "invalid_histogram"
	}
	count, before := cur.Values[countKey], prev.Values[countKey]
	_, exists := prev.Values[countKey]
	if status := pairStatus(cur, prev, family, count, before, exists); status != "ok" {
		return 0, status
	}
	deltaCount := count.Value - before.Value
	if deltaCount < 0 {
		return 0, "reset"
	}
	buckets := []bucket{}
	for member, key := range g.members {
		if !strings.HasPrefix(member, family+"_bucket:") {
			continue
		}
		v := cur.Values[key]
		old, ok := prev.Values[key]
		if status := pairStatus(cur, prev, family, v, old, ok); status != "ok" {
			return 0, "buckets_changed"
		}
		if v.Value < old.Value {
			return 0, "reset"
		}
		bound, err := strconv.ParseFloat(cur.Definitions[key].Labels["le"], 64)
		if err != nil || math.IsNaN(bound) {
			return 0, "invalid_histogram"
		}
		buckets = append(buckets, bucket{bound, v.Value - old.Value})
	}
	// Disappearing buckets must also invalidate the interval.
	if g.oldBuckets != len(buckets) {
		return 0, "buckets_changed"
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].bound < buckets[j].bound })
	if len(buckets) < 2 || !math.IsInf(buckets[len(buckets)-1].bound, 1) || buckets[len(buckets)-1].count != deltaCount {
		return 0, "invalid_histogram"
	}
	for i, b := range buckets {
		if i > 0 && (b.bound <= buckets[i-1].bound || b.count < buckets[i-1].count) {
			return 0, "invalid_histogram"
		}
	}
	if view == "rate" {
		return deltaCount / cur.At.Sub(prev.At).Seconds(), "ok"
	}
	if deltaCount == 0 {
		return 0, "no_observations"
	}
	if view == "mean" {
		key, ok := g.members[family+"_sum:"]
		if !ok {
			return 0, "invalid_histogram"
		}
		old, exists := prev.Values[key]
		now := cur.Values[key]
		if status := pairStatus(cur, prev, family, now, old, exists); status != "ok" {
			return 0, status
		}
		return (now.Value - old.Value) / deltaCount, "ok"
	}
	q := map[string]float64{"p50": .5, "p95": .95, "p99": .99}[view]
	if q == 0 {
		return 0, "unsupported_view"
	}
	rank := q * deltaCount
	for i, b := range buckets {
		if b.count < rank {
			continue
		}
		if math.IsInf(b.bound, 1) {
			return buckets[i-1].bound, "ok"
		}
		if i == 0 && b.bound <= 0 {
			return b.bound, "ok"
		}
		lower, preceding := 0.0, 0.0
		if i > 0 {
			lower = buckets[i-1].bound
			preceding = buckets[i-1].count
		}
		return lower + (b.bound-lower)*(rank-preceding)/(b.count-preceding), "ok"
	}
	return 0, "invalid_histogram"
}
