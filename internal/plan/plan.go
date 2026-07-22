// Package plan defines the substrate-independent intermediate representation (IR)
// for a Vikasa infrastructure deployment. It knows nothing about Kubernetes,
// YAML, or any other substrate; those concerns live in downstream renderers.
package plan

import (
	"fmt"
	"sort"

	"github.com/Vikasa2M/vikasa-infra/internal/naming"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// Plan is the top-level IR produced from a topology spec.
type Plan struct {
	DOT     string
	Streams []Stream
	DNS     []DNSRecord
}

// Tier identifies a stream's role in the DOT hierarchy.
type Tier string

const (
	TierRegional Tier = "regional"
	TierCentral  Tier = "central"
	TierDMZ      Tier = "dmz"
)

// Per-tier stream bounds. Conservative hardcoded defaults pending the load
// test (docs/capacity-model.md §6); spec-configurable limits are a later plan.
const (
	gib = int64(1) << 30

	regionalMaxBytes = 50 * gib
	centralMaxBytes  = 20 * gib
	dmzMaxBytes      = 10 * gib

	centralMaxAge   = "15m" // central is aggregation/routing, not the archive
	dmzMaxAge       = "1h"
	dmzDedupeWindow = "5m" // must be <= dmzMaxAge (NATS: Duplicates <= MaxAge)
)

// Wave is the provisioning order (Argo sync-wave) implied by the tier:
// central=0, regional=1, dmz=2 (the DMZ egress is provisioned last).
func (t Tier) Wave() int {
	switch t {
	case TierRegional:
		return 1
	case TierDMZ:
		return 2
	}
	return 0 // central
}

// Stream describes a single NATS JetStream stream to provision.
type Stream struct {
	Name       string // NATS stream name
	Cluster    string // cluster id (placement)
	JSDomain   string // that cluster's js-domain
	Replicas   int
	MaxAge     string
	MaxBytes   int64  // per-stream storage bound in bytes; must be > 0 (finding C2)
	Duplicates string // dedup window (e.g. "5m"); set on the DMZ stream only (finding C4)
	// RePublish echoes committed messages onto the core-NATS bus for the
	// fan-out consumer tier (Decision D3). Set on the DMZ stream only — it is
	// source-only, so an identity echo is loop-safe. "" = off.
	RePublishSource string
	RePublishDest   string
	Tier            Tier
	Sources         []Source // cross-domain sources (populated for central stream)
}

// Source is a cross-domain JetStream source entry.
type Source struct {
	Name            string
	Domain          string
	FilterSubject   string
	TransformSource string // nack CRD subjectTransforms[].source; mutually exclusive with FilterSubject
	TransformDest   string // nack CRD subjectTransforms[].dest
}

// DNSRecord is a DNS CNAME/A record to publish.
type DNSRecord struct {
	Name   string
	Target string
}

// partEntry is a (districtID, partitionID, clusterID, streamName) tuple
// collected during the regional pass; streamName is precomputed so downstream
// sorting/collision-checking/building never recomputes PartitionStreamName.
type partEntry struct {
	districtID  string
	partitionID string
	clusterID   string
	streamName  string
}

// Build constructs a deterministic Plan from a loaded topology Root.
// It returns a descriptive error if the topology is malformed for plan-building.
func Build(root *topology.Root) (*Plan, error) {
	if root == nil || root.Topology == nil {
		return nil, fmt.Errorf("plan.Build: nil topology")
	}
	t := root.Topology

	if t.Dot == nil {
		return nil, fmt.Errorf("plan.Build: topology.dot is nil")
	}
	dot := *t.Dot

	if t.Central == nil {
		return nil, fmt.Errorf("plan.Build: topology.central is nil")
	}
	if t.Central.Cluster == nil {
		return nil, fmt.Errorf("plan.Build: topology.central.cluster is nil")
	}

	// Helper to look up a cluster, failing with a clear message if absent.
	getCluster := func(id string) (*topology.Cluster, error) {
		c, ok := t.Cluster[id]
		if !ok || c == nil {
			return nil, fmt.Errorf("plan.Build: cluster %q not found in topology", id)
		}
		if c.JsDomain == nil {
			return nil, fmt.Errorf("plan.Build: cluster %q: js-domain is nil", id)
		}
		return c, nil
	}

	// --- Regional partition streams ---

	// Collect (districtID, partitionID, partition) triples so we can sort them.
	// The stream name is computed once here (decorate-sort) rather than being
	// recomputed in the sort comparator and collision loop below.
	var parts []partEntry

	districtSpace := map[string]string{} // districtID -> resolved subject space (for DMZ share matching)
	for distID, district := range t.District {
		if district == nil {
			continue
		}
		districtSpace[distID] = SubjectSpace(dot, distID, district.SubjectPrefix)
		for partID, partition := range district.Partition {
			if partition == nil {
				continue
			}
			if partition.Cluster == nil {
				return nil, fmt.Errorf("plan.Build: district %q partition %q: cluster is nil", distID, partID)
			}
			parts = append(parts, partEntry{
				districtID:  distID,
				partitionID: partID,
				clusterID:   *partition.Cluster,
				streamName:  PartitionStreamName(dot, distID, partID),
			})
		}
	}

	// Sort for determinism.
	sort.Slice(parts, func(i, j int) bool { return parts[i].streamName < parts[j].streamName })

	// Stream names must be unique: sanitize() maps '/' and '-' to the same
	// '_', so distinct partition ids can collide. Downstream name-keyed maps
	// (diff, cabinet attachment) would silently drop one of the two.
	for i := 1; i < len(parts); i++ {
		if parts[i].streamName == parts[i-1].streamName {
			return nil, fmt.Errorf("plan.Build: partitions %s/%q and %s/%q collide on stream name %q",
				parts[i-1].districtID, parts[i-1].partitionID, parts[i].districtID, parts[i].partitionID, parts[i].streamName)
		}
	}

	regionalStreams, centralSources, dns, err := buildRegional(dot, parts, getCluster)
	if err != nil {
		return nil, err
	}

	// --- Central shards (one per partition) ---

	centralShards, centralCluster, centralByDistrict, err := buildCentralShards(dot, t.Central, parts, centralSources, getCluster)
	if err != nil {
		return nil, err
	}

	// --- DMZ egress stream (Wave 2) ---

	allStreams := append(centralShards, regionalStreams...)

	dmzStream, hasDMZ, err := buildDMZ(dot, t.DMZ, centralCluster, centralByDistrict, districtSpace, getCluster)
	if err != nil {
		return nil, err
	}
	if hasDMZ {
		allStreams = append(allStreams, dmzStream)
	}

	// --- Assemble final stream slice sorted by (Wave, Name) ---
	// central=0, regional=1, dmz=2; within each wave sorted by Name.
	sort.SliceStable(allStreams, func(i, j int) bool {
		if allStreams[i].Tier.Wave() != allStreams[j].Tier.Wave() {
			return allStreams[i].Tier.Wave() < allStreams[j].Tier.Wave()
		}
		return allStreams[i].Name < allStreams[j].Name
	})

	return &Plan{
		DOT:     dot,
		Streams: allStreams,
		DNS:     dns,
	}, nil
}

// buildRegional constructs the regional partition streams, their
// corresponding central-stream sources (one per partition, in parts order),
// and their leaf DNS records. parts must already be sorted and collision-
// checked by stream name (see Build).
func buildRegional(dot string, parts []partEntry, getCluster func(string) (*topology.Cluster, error)) ([]Stream, []Source, []DNSRecord, error) {
	var regionalStreams []Stream
	var dns []DNSRecord

	// Leaf DNS names omit the district, so distinct partition ids can collide
	// after the '/'->'-' transform (same class as the stream-name guard above).
	// The name is operationally load-bearing (cabinets dial it): reject rather
	// than rename or silently emit conflicting records.
	dnsSeen := map[string]string{} // leaf DNS name -> "<district>/<partition>"

	// Sources for the central stream — one per partition stream, in the same order.
	var centralSources []Source

	for _, p := range parts {
		cluster, err := getCluster(p.clusterID)
		if err != nil {
			return nil, nil, nil, err
		}

		if cluster.LeafEndpoint == nil {
			return nil, nil, nil, fmt.Errorf("plan.Build: cluster %q: leaf-endpoint is nil (required for partition DNS)", p.clusterID)
		}

		jsDomain := *cluster.JsDomain
		leafEndpoint := *cluster.LeafEndpoint

		regionalStreams = append(regionalStreams, Stream{
			Name:     p.streamName,
			Cluster:  p.clusterID,
			JSDomain: jsDomain,
			Replicas: 3,
			MaxAge:   "6h",
			MaxBytes: regionalMaxBytes,
			Tier:     "regional",
		})

		centralSources = append(centralSources, Source{
			Name:          p.streamName,
			Domain:        jsDomain,
			FilterSubject: "",
		})

		// DNS record: leaf-<dot>-<partDNS>.nats.vikasa.<dot>
		dnsName := naming.LeafDNSName(dot, p.partitionID)

		key := p.districtID + "/" + p.partitionID
		if prev, dup := dnsSeen[dnsName]; dup {
			return nil, nil, nil, fmt.Errorf("plan.Build: partitions %s and %s collide on leaf DNS name %q (their leaf-DNS segments collide)", prev, key, dnsName)
		}
		dnsSeen[dnsName] = key

		dns = append(dns, DNSRecord{
			Name:   dnsName,
			Target: leafEndpoint,
		})
	}

	// centralSources already in sorted parts order (see collision guard above
	// in Build); no re-sort needed.
	sort.Slice(dns, func(i, j int) bool {
		return dns[i].Name < dns[j].Name
	})

	return regionalStreams, centralSources, dns, nil
}

// buildCentralShards constructs one central aggregation shard per partition, each
// sourcing exactly that partition's regional stream (finding C1: central is
// sharded so no single leader holds the whole DOT). parts and centralSources are
// in the same sorted order (see buildRegional), so shard[i] sources centralSources[i].
// Returns the shards, the resolved central cluster (reused by buildDMZ), and a
// districtID -> ordered shard names map for DMZ share fan-out.
func buildCentralShards(dot string, central *topology.Central, parts []partEntry, centralSources []Source, getCluster func(string) (*topology.Cluster, error)) ([]Stream, *topology.Cluster, map[string][]string, error) {
	centralClusterID := *central.Cluster
	centralCluster, err := getCluster(centralClusterID)
	if err != nil {
		return nil, nil, nil, err
	}
	centralReplicas := 3 // R3 default (finding E5); spec-overridable via central.replicas
	if central.Replicas != nil {
		centralReplicas = int(*central.Replicas)
	}
	centralJSDomain := *centralCluster.JsDomain

	shards := make([]Stream, 0, len(parts))
	byDistrict := map[string][]string{}
	for i, part := range parts {
		name := CentralShardStreamName(dot, part.districtID, part.partitionID)
		shards = append(shards, Stream{
			Name:     name,
			Cluster:  centralClusterID,
			JSDomain: centralJSDomain,
			Replicas: centralReplicas,
			MaxAge:   centralMaxAge,
			MaxBytes: centralMaxBytes,
			Tier:     "central",
			Sources:  []Source{centralSources[i]},
		})
		byDistrict[part.districtID] = append(byDistrict[part.districtID], name)
	}
	// byDistrict lists follow sorted parts order; no re-sort needed.
	return shards, centralCluster, byDistrict, nil
}

// buildDMZ constructs the DMZ egress stream (Wave 2), gated on dmz != nil &&
// dmz.Cluster != nil; ok is false when there is no DMZ block to build.
// centralCluster is the already-resolved central cluster (see buildCentral),
// reused here for the DMZ sources' Domain.
func buildDMZ(dot string, dmz *topology.DMZ, centralCluster *topology.Cluster, centralByDistrict map[string][]string, districtSpace map[string]string, getCluster func(string) (*topology.Cluster, error)) (stream Stream, ok bool, err error) {
	if dmz == nil || dmz.Cluster == nil {
		return Stream{}, false, nil
	}

	dmzCluster, err := getCluster(*dmz.Cluster)
	if err != nil {
		return Stream{}, false, err
	}
	dmzReplicas := 3
	if dmz.Replicas != nil {
		dmzReplicas = int(*dmz.Replicas)
	}
	centralJSDomain := *centralCluster.JsDomain

	// Resolve the district a share draws from by matching its `from` subject
	// against each district's subject space; subject spaces are disjoint so at
	// most one matches. We then fan the share across that district's central shards.
	districtOf := func(from string) (string, bool) {
		for distID, space := range districtSpace {
			if UnderPrefix(from, space) {
				return distID, true
			}
		}
		return "", false
	}

	var dmzSources []Source
	for _, s := range dmz.Shares {
		if s.From == nil || s.As == nil {
			continue
		}
		distID, matched := districtOf(*s.From)
		if !matched {
			return Stream{}, false, fmt.Errorf("plan.Build: DMZ share from %q is not under any district subject space", *s.From)
		}
		// Central is sharded per partition (finding C1): one source per shard,
		// each carrying this share's transform. Shard order is the sorted parts order.
		for _, shardName := range centralByDistrict[distID] {
			dmzSources = append(dmzSources, Source{
				Name:            shardName,
				Domain:          centralJSDomain,
				TransformSource: *s.From,
				TransformDest:   *s.As,
			})
		}
	}
	return Stream{
		Name:            DMZStreamName(dot),
		Cluster:         *dmz.Cluster,
		JSDomain:        *dmzCluster.JsDomain,
		Replicas:        dmzReplicas,
		MaxAge:          dmzMaxAge,
		MaxBytes:        dmzMaxBytes,
		Duplicates:      dmzDedupeWindow,
		RePublishSource: naming.RootSpace(), // core-NATS fan-out echo (D3); loop-safe (source-only stream)
		RePublishDest:   naming.RootSpace(),
		Tier:            "dmz",
		Sources:         dmzSources,
	}, true, nil
}
