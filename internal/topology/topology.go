// Package topology is the hand-written topology model for vikasa-infra.
// It loads a per-DOT deployment spec (the JSON under examples/*.json), indexes
// list entries by their id, and validates structure + placement. It replaces a
// previously ygot/YANG-generated model; the field shapes (id-keyed maps, pointer
// scalars, substrate enum) are preserved so downstream packages are unchanged
// except for shorter type names.
package topology

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Vikasa2M/vikasa-infra/internal/naming"
)

// SubstrateType is where a cluster physically runs.
type SubstrateType int64

const (
	SubstrateUnset      SubstrateType = 0
	SubstrateKubernetes SubstrateType = 1
	SubstrateBareMetal  SubstrateType = 2
)

// Root is the document root.
type Root struct {
	Topology *Topology
}

// Topology is the per-DOT deployment topology.
type Topology struct {
	Central  *Central
	Cluster  map[string]*Cluster  // keyed by cluster id
	District map[string]*District // keyed by district id
	DMZ      *DMZ
	Dot      *string
}

// Central is the aggregation tier placement.
type Central struct {
	Cluster  *string
	Replicas *uint8
}

// Cluster is a NATS cluster (placement target).
type Cluster struct {
	Id           *string
	JsDomain     *string
	LeafEndpoint *string
	Substrate    *Substrate
}

// Substrate is where a cluster physically lives.
type Substrate struct {
	Context   *string
	Namespace *string
	Hosts     []string
	Type      SubstrateType
}

// DMZ is the external-sharing boundary (its own cluster). It re-publishes a
// filtered subset of central traffic under public subjects.
type DMZ struct {
	Cluster  *string
	Replicas *uint8
	Shares   []*Share
}

// Share maps an internal subject (From) to a public subject (As) for one named
// external consumer. Filter + remap only; aggregation/redaction are out of scope.
type Share struct {
	Consumer *string
	From     *string
	As       *string
}

// DefaultShareAs is the recommended public subject for a consumer when As is
// omitted: vikasa.<dot>.share.<consumer>.> (a documented, unenforced convention).
func DefaultShareAs(dot, consumer string) string {
	return naming.ShareSpace(dot) + consumer + ".>"
}

// District is a stable org unit (≡ region/area; the id carries the DOT's naming).
type District struct {
	Id            *string
	Partition     map[string]*Partition // keyed by partition id
	SubjectPrefix *string
}

// Partition is the scaling/placement atom within a district.
type Partition struct {
	Cluster *string
	Id      *string
}

// ---- JSON wire layer (arrays keyed by id) → indexed public model ----

type wireRoot struct {
	Topology *wireTopology `json:"vikasa-infra-topology:topology"`
}

type wireTopology struct {
	Dot      *string        `json:"dot"`
	Cluster  []wireCluster  `json:"cluster"`
	District []wireDistrict `json:"district"`
	Central  *Central       `json:"central"`
	DMZ      *wireDMZ       `json:"dmz"`
}

type wireDMZ struct {
	Cluster  *string      `json:"cluster"`
	Replicas *uint8       `json:"replicas"`
	Shares   []*wireShare `json:"shares"`
}

type wireShare struct {
	Consumer *string `json:"consumer"`
	From     *string `json:"from"`
	As       *string `json:"as"`
}

type wireCluster struct {
	Id           *string        `json:"id"`
	JsDomain     *string        `json:"js-domain"`
	LeafEndpoint *string        `json:"leaf-endpoint"`
	Substrate    *wireSubstrate `json:"substrate"`
}

type wireSubstrate struct {
	Type      string   `json:"type"`
	Context   *string  `json:"context"`
	Namespace *string  `json:"namespace"`
	Hosts     []string `json:"hosts"`
}

type wireDistrict struct {
	Id            *string         `json:"id"`
	SubjectPrefix *string         `json:"subject-prefix"`
	Partition     []wirePartition `json:"partition"`
}

type wirePartition struct {
	Id      *string `json:"id"`
	Cluster *string `json:"cluster"`
}

func substrateType(s string) SubstrateType {
	switch s {
	case SubstrateKubernetes.String():
		return SubstrateKubernetes
	case SubstrateBareMetal.String():
		return SubstrateBareMetal
	default:
		return SubstrateUnset
	}
}

// String returns the wire/label form of the substrate type — the single
// source for the "kubernetes"/"bare-metal" spellings used in specs and
// rendered output.
func (s SubstrateType) String() string {
	switch s {
	case SubstrateKubernetes:
		return "kubernetes"
	case SubstrateBareMetal:
		return "bare-metal"
	default:
		return "unset"
	}
}

// UnmarshalJSON converts the RFC-7951-flavored wire form (id-keyed arrays, a
// module-qualified top key) into the indexed public model.
func (r *Root) UnmarshalJSON(b []byte) error {
	var w wireRoot
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	if w.Topology == nil {
		r.Topology = nil
		return nil
	}
	t := &Topology{
		Dot:     w.Topology.Dot,
		Central: w.Topology.Central,
	}
	if len(w.Topology.Cluster) > 0 {
		t.Cluster = make(map[string]*Cluster, len(w.Topology.Cluster))
		for i := range w.Topology.Cluster {
			wc := w.Topology.Cluster[i]
			if wc.Id == nil {
				return fmt.Errorf("cluster[%d]: missing id", i)
			}
			if _, dup := t.Cluster[*wc.Id]; dup {
				return fmt.Errorf("duplicate cluster id %q", *wc.Id)
			}
			c := &Cluster{Id: wc.Id, JsDomain: wc.JsDomain, LeafEndpoint: wc.LeafEndpoint}
			if wc.Substrate != nil {
				c.Substrate = &Substrate{
					Context:   wc.Substrate.Context,
					Namespace: wc.Substrate.Namespace,
					Hosts:     wc.Substrate.Hosts,
					Type:      substrateType(wc.Substrate.Type),
				}
			}
			t.Cluster[*wc.Id] = c
		}
	}
	if len(w.Topology.District) > 0 {
		t.District = make(map[string]*District, len(w.Topology.District))
		for i := range w.Topology.District {
			wd := w.Topology.District[i]
			if wd.Id == nil {
				return fmt.Errorf("district[%d]: missing id", i)
			}
			if _, dup := t.District[*wd.Id]; dup {
				return fmt.Errorf("duplicate district id %q", *wd.Id)
			}
			d := &District{Id: wd.Id, SubjectPrefix: wd.SubjectPrefix}
			if len(wd.Partition) > 0 {
				d.Partition = make(map[string]*Partition, len(wd.Partition))
				for j := range wd.Partition {
					wp := wd.Partition[j]
					if wp.Id == nil {
						return fmt.Errorf("district %q partition[%d]: missing id", *wd.Id, j)
					}
					if _, dup := d.Partition[*wp.Id]; dup {
						return fmt.Errorf("district %q: duplicate partition id %q", *wd.Id, *wp.Id)
					}
					d.Partition[*wp.Id] = &Partition{Id: wp.Id, Cluster: wp.Cluster}
				}
			}
			t.District[*wd.Id] = d
		}
	}
	if w.Topology.DMZ != nil {
		d := &DMZ{Cluster: w.Topology.DMZ.Cluster, Replicas: w.Topology.DMZ.Replicas}
		for _, ws := range w.Topology.DMZ.Shares {
			s := &Share{Consumer: ws.Consumer, From: ws.From, As: ws.As}
			if s.As == nil && s.Consumer != nil && t.Dot != nil {
				def := DefaultShareAs(*t.Dot, *s.Consumer)
				s.As = &def
			}
			d.Shares = append(d.Shares, s)
		}
		t.DMZ = d
	}
	r.Topology = t
	return nil
}

// Unmarshal parses spec JSON into root (kept for load.go compatibility).
func Unmarshal(data []byte, root *Root) error {
	return json.Unmarshal(data, root)
}

// DefaultSubjectPrefix is a district's subject boundary when none is declared.
// The only fixed anchor is vikasa.<dot>.; everything below is the DOT's.
func DefaultSubjectPrefix(dot, districtID string) string {
	return naming.DefaultSubjectPrefix(dot, districtID)
}

// PartitionIndex maps every partition id to its owning district id, erroring
// on a partition id that appears in more than one district (partition ids are
// a single flat namespace: cabinets reference them without a district
// qualifier). Shared by plan.AttachCabinets and issuance so the two tools
// resolve inventories identically.
func (t *Topology) PartitionIndex() (map[string]string, error) {
	idx := map[string]string{}
	for _, distID := range sortedKeys(t.District) {
		d := t.District[distID]
		if d == nil {
			continue
		}
		for _, partID := range sortedKeys(d.Partition) {
			if _, dup := idx[partID]; dup {
				return nil, fmt.Errorf("duplicate partition id %q across districts", partID)
			}
			idx[partID] = distID
		}
	}
	return idx, nil
}

// sortedKeys returns m's keys in ascending lexicographic order, so callers
// that report the first violation found during iteration do so
// deterministically instead of depending on Go's randomized map order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// partitionIDRE additionally permits '/' separators between tokens (the
// conventional d7/0 form). '_' stays illegal everywhere: stream-name
// sanitization maps '/' and '-' to '_', so a literal underscore could forge
// another partition's stream name.
var partitionIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(/[a-z0-9][a-z0-9-]*)*$`)

// Validate checks structural constraints the YANG schema used to enforce:
// required fields, the token pattern, the substrate enum, and replica range.
// Referential (leafref) checks live in load.go's validatePlacement.
func (r *Root) Validate() error {
	if r.Topology == nil {
		return fmt.Errorf("topology: missing")
	}
	t := r.Topology
	if t.Dot == nil || !tokenRE.MatchString(*t.Dot) {
		return fmt.Errorf("topology.dot: missing or not a token [a-z0-9-]")
	}
	for _, id := range sortedKeys(t.Cluster) {
		c := t.Cluster[id]
		if !tokenRE.MatchString(id) {
			return fmt.Errorf("cluster %q: id is not a token", id)
		}
		if c.JsDomain == nil || !tokenRE.MatchString(*c.JsDomain) {
			return fmt.Errorf("cluster %q: js-domain missing or not a token", id)
		}
		if c.LeafEndpoint == nil || *c.LeafEndpoint == "" {
			return fmt.Errorf("cluster %q: leaf-endpoint is required", id)
		}
		if c.Substrate == nil || c.Substrate.Type == SubstrateUnset {
			return fmt.Errorf("cluster %q: substrate.type must be kubernetes or bare-metal", id)
		}
	}
	for _, id := range sortedKeys(t.District) {
		d := t.District[id]
		if !tokenRE.MatchString(id) {
			return fmt.Errorf("district %q: id is not a token", id)
		}
		for _, partID := range sortedKeys(d.Partition) {
			if !partitionIDRE.MatchString(partID) {
				return fmt.Errorf("district %q: partition id %q is not a token (lowercase alphanumerics, '-', with optional '/' separators)", id, partID)
			}
		}
		if d.SubjectPrefix != nil {
			anchor := naming.Anchor(*t.Dot)
			if len(*d.SubjectPrefix) < len(anchor) || (*d.SubjectPrefix)[:len(anchor)] != anchor {
				return fmt.Errorf("district %q: subject-prefix %q must be anchored at %q", id, *d.SubjectPrefix, anchor)
			}
			// Boundary checks (naming.UnderPrefix) match on the token before
			// ".>"; a prefix without it would degrade to exact-match only.
			if !strings.HasSuffix(*d.SubjectPrefix, ".>") {
				return fmt.Errorf("district %q: subject-prefix %q must end with %q (a subject-space boundary)", id, *d.SubjectPrefix, ".>")
			}
		}
	}
	if t.Central == nil || t.Central.Cluster == nil {
		return fmt.Errorf("central.cluster is required")
	}
	if t.Central.Replicas != nil && (*t.Central.Replicas < 1 || *t.Central.Replicas > 5) {
		return fmt.Errorf("central.replicas %d out of range 1..5", *t.Central.Replicas)
	}
	// Partition ids are a single flat namespace (cabinets reference them
	// without a district qualifier); reject cross-district reuse up front
	// rather than only when a cabinet-resolving path builds the index.
	if _, err := t.PartitionIndex(); err != nil {
		return err
	}
	if t.DMZ != nil {
		if t.DMZ.Cluster == nil {
			return fmt.Errorf("dmz.cluster is required when dmz is present")
		}
		if t.DMZ.Replicas != nil && (*t.DMZ.Replicas < 1 || *t.DMZ.Replicas > 5) {
			return fmt.Errorf("dmz.replicas %d out of range 1..5", *t.DMZ.Replicas)
		}
		// A dmz block exists to share something; an empty shares list would
		// provision an inert, sourceless egress stream.
		if len(t.DMZ.Shares) == 0 {
			return fmt.Errorf("dmz: at least one share is required (remove the dmz block if nothing is shared)")
		}
		for i, s := range t.DMZ.Shares {
			if s.Consumer == nil || *s.Consumer == "" {
				return fmt.Errorf("dmz.shares[%d]: consumer is required", i)
			}
			if s.From == nil || *s.From == "" {
				return fmt.Errorf("dmz share %q: from is required", *s.Consumer)
			}
			if s.As == nil || *s.As == "" {
				return fmt.Errorf("dmz share %q: as is required", *s.Consumer)
			}
			shareSpace := naming.ShareSpace(*t.Dot)
			peerSpace := naming.PeerSpace(*t.Dot)
			if !strings.HasPrefix(*s.As, shareSpace) && !strings.HasPrefix(*s.As, peerSpace) {
				return fmt.Errorf("dmz share %q: as %q must live under %q or %q (deny-by-default)", *s.Consumer, *s.As, shareSpace+">", peerSpace+">")
			}
		}
	}
	return nil
}
