package plan

import (
	"sort"
)

// StreamMove pairs a stream's old and new states (same Name).
type StreamMove struct {
	Old Stream
	New Stream
}

// DNSChange is a DNS record whose target changed.
type DNSChange struct {
	Name      string
	OldTarget string
	NewTarget string
}

// Delta is the categorized difference between two plans, keyed by stream name
// (cluster-independent) and DNS record name. It drives the rebalance runbook.
type Delta struct {
	DOT        string
	Added      []Stream     // stream name in new only
	Removed    []Stream     // stream name in old only
	Moved      []StreamMove // name in both; Cluster or JSDomain changed
	Modified   []StreamMove // name in both; same Cluster & JSDomain; Replicas/MaxAge/Sources changed
	DNSAdded   []DNSRecord
	DNSRemoved []DNSRecord
	DNSChanged []DNSChange
}

// Diff categorizes the change from oldP to newP. Both must be non-nil (the CLI
// always passes plans from Build).
func Diff(oldP, newP *Plan) *Delta {
	d := &Delta{DOT: newP.DOT}

	oldS := indexStreams(oldP.Streams)
	newS := indexStreams(newP.Streams)
	for name, ns := range newS {
		os, ok := oldS[name]
		if !ok {
			d.Added = append(d.Added, ns)
			continue
		}
		switch {
		case os.Cluster != ns.Cluster || os.JSDomain != ns.JSDomain:
			d.Moved = append(d.Moved, StreamMove{Old: os, New: ns})
		case streamConfigChanged(os, ns):
			d.Modified = append(d.Modified, StreamMove{Old: os, New: ns})
		}
	}
	for name, os := range oldS {
		if _, ok := newS[name]; !ok {
			d.Removed = append(d.Removed, os)
		}
	}

	oldR := indexDNS(oldP.DNS)
	newR := indexDNS(newP.DNS)
	for name, nr := range newR {
		or, ok := oldR[name]
		if !ok {
			d.DNSAdded = append(d.DNSAdded, nr)
			continue
		}
		if or.Target != nr.Target {
			d.DNSChanged = append(d.DNSChanged, DNSChange{Name: name, OldTarget: or.Target, NewTarget: nr.Target})
		}
	}
	for name, or := range oldR {
		if _, ok := newR[name]; !ok {
			d.DNSRemoved = append(d.DNSRemoved, or)
		}
	}

	// Determinism: sort every slice by name.
	sort.Slice(d.Added, func(i, j int) bool { return d.Added[i].Name < d.Added[j].Name })
	sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Name < d.Removed[j].Name })
	sort.Slice(d.Moved, func(i, j int) bool { return d.Moved[i].New.Name < d.Moved[j].New.Name })
	sort.Slice(d.Modified, func(i, j int) bool { return d.Modified[i].New.Name < d.Modified[j].New.Name })
	sort.Slice(d.DNSAdded, func(i, j int) bool { return d.DNSAdded[i].Name < d.DNSAdded[j].Name })
	sort.Slice(d.DNSRemoved, func(i, j int) bool { return d.DNSRemoved[i].Name < d.DNSRemoved[j].Name })
	sort.Slice(d.DNSChanged, func(i, j int) bool { return d.DNSChanged[i].Name < d.DNSChanged[j].Name })

	return d
}

// streamConfigChanged reports whether two streams with the same Name and
// placement (Cluster/JSDomain) differ in any change-tracked field. Sources are
// compared treating nil and empty as equal.
func streamConfigChanged(a, b Stream) bool {
	if a.Replicas != b.Replicas || a.MaxAge != b.MaxAge ||
		a.MaxBytes != b.MaxBytes || a.Duplicates != b.Duplicates ||
		a.RePublishSource != b.RePublishSource || a.RePublishDest != b.RePublishDest ||
		a.Tier != b.Tier {
		return true
	}
	if len(a.Sources) == 0 && len(b.Sources) == 0 {
		return false
	}
	return sourcesChanged(a.Sources, b.Sources)
}

// sourcesChanged is an explicit field-by-field comparison of two Source
// slices (length, then per-element Name/Domain/FilterSubject/
// TransformSource/TransformDest), used in place of reflect.DeepEqual on this
// diff hot path.
func sourcesChanged(a, b []Source) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Domain != b[i].Domain ||
			a[i].FilterSubject != b[i].FilterSubject ||
			a[i].TransformSource != b[i].TransformSource ||
			a[i].TransformDest != b[i].TransformDest {
			return true
		}
	}
	return false
}

// HasChanges reports whether the delta contains any change.
func (d *Delta) HasChanges() bool { return d.ChangeCount() > 0 }

// ChangeCount is the total number of changed items across all categories.
func (d *Delta) ChangeCount() int {
	return len(d.Added) + len(d.Removed) + len(d.Moved) + len(d.Modified) +
		len(d.DNSAdded) + len(d.DNSRemoved) + len(d.DNSChanged)
}

func indexStreams(ss []Stream) map[string]Stream {
	m := make(map[string]Stream, len(ss))
	for _, s := range ss {
		m[s.Name] = s
	}
	return m
}

func indexDNS(rs []DNSRecord) map[string]DNSRecord {
	m := make(map[string]DNSRecord, len(rs))
	for _, r := range rs {
		m[r.Name] = r
	}
	return m
}
