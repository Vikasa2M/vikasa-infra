//go:build integration

// Package integration contains live, embedded-NATS end-to-end tests for the
// Vikasa DMZ data path. These tests are gated behind the `integration` build
// tag and do NOT run in the default `go test ./...` unit pass.
//
// They exist to DISCOVER and PROVE the exact JetStream configuration the
// generator must emit for the DMZ egress stream (cross-domain sourcing of the
// central stream with an internal->public subject transform).
//
// Run with:
//
//	go test -tags integration ./test/integration/ -run TestDMZ -v
package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// srv wraps an embedded server together with the Options pointer it was built
// from. The server mutates that Options struct in place at start time to
// resolve ephemeral (-1) client and leafnode ports, so we keep the pointer to
// read the resolved ports back (the server type exposes no public Opts()
// accessor).
type srv struct {
	*server.Server
	opts *server.Options
}

// startServer writes the given NATS config text to a temp file, processes it
// into Options, and starts an embedded server. It returns the running server
// (with leafnode/client ports already resolved on its Options) and registers a
// cleanup to shut it down.
func startServer(t *testing.T, name, conf string) *srv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name+".conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatalf("%s: write conf: %v", name, err)
	}
	opts, err := server.ProcessConfigFile(path)
	if err != nil {
		t.Fatalf("%s: process conf: %v", name, err)
	}
	opts.NoLog = true
	opts.NoSigs = true
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("%s: new server: %v", name, err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatalf("%s: not ready for connections", name)
	}
	t.Cleanup(s.Shutdown)
	return &srv{Server: s, opts: opts}
}

// connectJS opens a client connection to s as the APP account user and returns
// a JetStream context bound to the local JS domain.
func connectJS(t *testing.T, s *srv, domain string) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	url := fmt.Sprintf("nats://app:app@127.0.0.1:%d", s.opts.Port)
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", s.Name(), err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream(nats.Domain(domain))
	if err != nil {
		t.Fatalf("jetstream ctx %s: %v", s.Name(), err)
	}
	return nc, js
}

// hubConf renders the config for a JetStream server that ACCEPTS leaf
// connections (a hub). It hosts the APP and SYS accounts; APP has JetStream
// enabled because it will host streams.
func hubConf(t *testing.T, name, domain string, leafPort int) string {
	return fmt.Sprintf(`
server_name: %s
listen: 127.0.0.1:-1
jetstream { domain: %q, store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
leafnodes { listen: 127.0.0.1:%d }
accounts {
  APP: { jetstream: enabled, users: [ { user: app, password: app } ] }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, name, domain, t.TempDir(), leafPort)
}

// leafConf renders the config for a JetStream server that SOLICITS a leaf
// connection up to a hub (for both the APP and SYS accounts), so that the APP
// account — and its $JS.<domain>.API surface — spans the two domains.
func leafConf(t *testing.T, name, domain string, hub *srv) string {
	hp := hub.opts.LeafNode.Port
	return fmt.Sprintf(`
server_name: %s
listen: 127.0.0.1:-1
jetstream { domain: %q, store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
leafnodes {
  remotes: [
    { url: "nats-leaf://app:app@127.0.0.1:%d", account: APP }
    { url: "nats-leaf://sys:sys@127.0.0.1:%d", account: SYS }
  ]
}
accounts {
  APP: { jetstream: enabled, users: [ { user: app, password: app } ] }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, name, domain, t.TempDir(), hp, hp)
}

// waitForStream polls until the named stream exists in the given JS context, or
// fails after the deadline. Cross-domain source plumbing can take a moment to
// settle, so callers use this before publishing/consuming.
func waitForStream(t *testing.T, js nats.JetStreamContext, stream string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := js.StreamInfo(stream); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("stream %q never became available", stream)
}

// expectMsg subscribes a transient pull-free consumer-less approach: it creates
// an ordered ephemeral consumer over the stream filtered to subject and waits
// for a message, returning its subject. Fails on timeout.
func expectMsg(t *testing.T, js nats.JetStreamContext, stream, filter string, timeout time.Duration) *nats.Msg {
	t.Helper()
	sub, err := js.PullSubscribe(filter, "", nats.BindStream(stream))
	if err != nil {
		t.Fatalf("pull subscribe %s/%s: %v", stream, filter, err)
	}
	defer sub.Unsubscribe()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs, err := sub.Fetch(1, nats.MaxWait(500*time.Millisecond))
		if err == nil && len(msgs) == 1 {
			_ = msgs[0].Ack()
			return msgs[0]
		}
	}
	t.Fatalf("no message on %s matching %q within %s", stream, filter, timeout)
	return nil
}

// countMsgs drains up to want messages (or until timeout) on the filter and
// returns the subjects received. Used to probe fan-out / duplication behavior.
func countMsgs(t *testing.T, js nats.JetStreamContext, stream, filter string, want int, timeout time.Duration) []string {
	t.Helper()
	sub, err := js.PullSubscribe(filter, "", nats.BindStream(stream))
	if err != nil {
		t.Fatalf("pull subscribe %s/%s: %v", stream, filter, err)
	}
	defer sub.Unsubscribe()
	var got []string
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && len(got) < want {
		msgs, err := sub.Fetch(want-len(got), nats.MaxWait(500*time.Millisecond))
		if err != nil {
			continue
		}
		for _, m := range msgs {
			_ = m.Ack()
			got = append(got, m.Subject)
		}
	}
	return got
}

// ---------------------------------------------------------------------------
// TEST 1: the riskiest hop — central -> dmz cross-domain REMAP.
// ---------------------------------------------------------------------------

// TestDMZ_CentralToDMZRemap proves a DMZ-domain stream can source the
// core-domain central stream cross-domain (External APIPrefix "$JS.core.API")
// and remap the internal subject space (vikasa.exdot.d1.>) onto the public
// share subject space (vikasa.exdot.share.research.>) via a per-source
// subject_transform. It also probes the overlapping-share fan-out question for
// the hwy9 corridor (which must reach BOTH research and peer share spaces).
func TestDMZ_CentralToDMZRemap(t *testing.T) {
	core := startServer(t, "core", hubConf(t, "core", "core", -1))
	dmz := startServer(t, "dmz", leafConf(t, "dmz", "dmz", core))

	checkLeafConnected(t, core, dmz)

	_, coreJS := connectJS(t, core, "core")
	dmzNC, dmzJS := connectJS(t, dmz, "dmz")

	// Central stream lives on core, holding the internal district subject space.
	if _, err := coreJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_CENTRAL",
		Subjects:  []string{"vikasa.exdot.d1.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("add central stream: %v", err)
	}

	// First, record the constraint that drives the whole design: a SINGLE source
	// may NOT carry multiple subject_transforms whose source filters overlap.
	// The two real shares overlap (peer's vikasa.exdot.d1.hwy9.> is a subset of
	// research's vikasa.exdot.d1.>), so they cannot live in one source.
	_, overlapErr := dmzJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_DMZ_OVERLAP_PROBE",
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
		Sources: []*nats.StreamSource{{
			Name:     "VIKASA_EXDOT_CENTRAL",
			External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
			SubjectTransforms: []nats.SubjectTransformConfig{
				{Source: "vikasa.exdot.d1.>", Destination: "vikasa.exdot.share.research.>"},
				{Source: "vikasa.exdot.d1.hwy9.>", Destination: "vikasa.peer.exdot.hwy9.>"},
			},
		}},
	})
	if overlapErr == nil {
		t.Fatalf("expected overlapping subject_transforms to be rejected, but stream was created")
	}
	t.Logf("FINDING: overlapping transforms in ONE source are REJECTED: %v", overlapErr)
	t.Logf("=> overlapping shares (research d1.> and peer d1.hwy9.>) MUST use SEPARATE sources.")

	// DMZ egress stream: sources central CROSS-DOMAIN with a single per-source
	// subject_transform that remaps the internal district space onto the public
	// research share space. This is the core remap config under test.
	if _, err := dmzJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_DMZ",
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
		Sources: []*nats.StreamSource{{
			Name:     "VIKASA_EXDOT_CENTRAL",
			External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
			SubjectTransforms: []nats.SubjectTransformConfig{
				{Source: "vikasa.exdot.d1.>", Destination: "vikasa.exdot.share.research.>"},
			},
		}},
	}); err != nil {
		t.Fatalf("add dmz stream (one source, single transform): %v", err)
	}

	waitForStream(t, dmzJS, "VIKASA_EXDOT_DMZ")

	// Publish district messages at central.
	if _, err := coreJS.Publish("vikasa.exdot.d1.001.signals", []byte("sig-001")); err != nil {
		t.Fatalf("publish d1.001: %v", err)
	}
	if _, err := coreJS.Publish("vikasa.exdot.d1.hwy9.mm42.flow", []byte("hwy9-flow")); err != nil {
		t.Fatalf("publish d1.hwy9: %v", err)
	}

	// Assert the message arrived ONLY under the public research subject
	// (transformed), proving the remap works cross-domain.
	m := expectMsg(t, dmzJS, "VIKASA_EXDOT_DMZ", "vikasa.exdot.share.research.001.signals", 15*time.Second)
	if string(m.Data) != "sig-001" {
		t.Fatalf("research remap: wrong payload %q", m.Data)
	}
	t.Logf("REMAP OK: vikasa.exdot.d1.001.signals -> %s", m.Subject)

	// The hwy9 message is in-scope for the research transform, so it too is
	// remapped under share.research (as ...research.hwy9.mm42.flow).
	dmzNC.Flush()
	mi := expectMsg(t, dmzJS, "VIKASA_EXDOT_DMZ", "vikasa.exdot.share.research.hwy9.>", 15*time.Second)
	t.Logf("REMAP OK (hwy9 under research): %s", mi.Subject)
}

// TestDMZ_SeparateSourcesFanout determines whether the overlapping hwy9 share
// can be delivered to BOTH the research and peer public spaces by using TWO
// sources that both reference the same upstream stream name
// ("VIKASA_EXDOT_CENTRAL") with distinct single transforms — and whether NATS
// permits two sources with the same upstream name at all.
func TestDMZ_SeparateSourcesFanout(t *testing.T) {
	core := startServer(t, "core", hubConf(t, "core", "core", -1))
	dmz := startServer(t, "dmz", leafConf(t, "dmz", "dmz", core))
	checkLeafConnected(t, core, dmz)

	_, coreJS := connectJS(t, core, "core")
	_, dmzJS := connectJS(t, dmz, "dmz")

	if _, err := coreJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_CENTRAL",
		Subjects:  []string{"vikasa.exdot.d1.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("add central stream: %v", err)
	}

	// Try TWO sources, both named after the real upstream stream, each with a
	// single distinct transform. If NATS rejects duplicate source names, the
	// error is itself the finding (drives generator toward one-source design).
	_, err := dmzJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_DMZ_FANOUT",
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
		Sources: []*nats.StreamSource{
			{
				Name:     "VIKASA_EXDOT_CENTRAL",
				External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
				SubjectTransforms: []nats.SubjectTransformConfig{
					{Source: "vikasa.exdot.d1.>", Destination: "vikasa.exdot.share.research.>"},
				},
			},
			{
				Name:     "VIKASA_EXDOT_CENTRAL",
				External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
				SubjectTransforms: []nats.SubjectTransformConfig{
					{Source: "vikasa.exdot.d1.hwy9.>", Destination: "vikasa.peer.exdot.hwy9.>"},
				},
			},
		},
	})
	if err != nil {
		// This is NOT an acceptable server behavior anymore: the generator's
		// emitted DMZ config (see cmd/gen/testdata/golden-dmz and ARCHITECTURE §7)
		// RELIES on duplicate-upstream-name sources to fan an overlapping share
		// out to multiple public spaces. If the server rejects it, the shipped
		// design is broken and this must fail loudly, not pass green.
		t.Fatalf("duplicate-upstream-name sources REJECTED (%v) — but the generator's "+
			"DMZ egress design depends on same-upstream fan-out (golden-dmz, ARCHITECTURE §7); "+
			"the emitted config would fail against this server version", err)
	}
	t.Logf("FINDING: duplicate-upstream-name sources ACCEPTED.")

	waitForStream(t, dmzJS, "VIKASA_EXDOT_DMZ_FANOUT")
	if _, err := coreJS.Publish("vikasa.exdot.d1.hwy9.mm42.flow", []byte("hwy9-flow")); err != nil {
		t.Fatalf("publish hwy9: %v", err)
	}
	research := countMsgs(t, dmzJS, "VIKASA_EXDOT_DMZ_FANOUT", "vikasa.exdot.share.research.hwy9.>", 1, 8*time.Second)
	peer := countMsgs(t, dmzJS, "VIKASA_EXDOT_DMZ_FANOUT", "vikasa.peer.exdot.hwy9.>", 1, 8*time.Second)
	t.Logf("FAN-OUT (two sources, same upstream name): research=%d peer=%d", len(research), len(peer))
	if len(research) < 1 || len(peer) < 1 {
		t.Fatalf("fan-out incomplete: research=%d peer=%d (expected >=1 each)", len(research), len(peer))
	}
	t.Logf("FINDING: two same-name sources DO fan out hwy9 to both research and peer.")
}

// TestDMZ_OutOfScopeNeverCrosses is the NEGATIVE isolation proof: the DMZ
// egress stream must contain ONLY what the share transforms explicitly export,
// and NOTHING under the internal subject forms. This is the security property
// the whole DMZ design exists for — a remap test alone can pass while the
// stream also leaks unshared subjects, so we prove absence, not just presence.
//
// Setup: central holds TWO district spaces (vikasa.exdot.d1.> and
// vikasa.exdot.d2.>); the ONLY share transform exports a strict SUBSET
// (vikasa.exdot.d1.hwy9.> -> vikasa.peer.exdot.hwy9.>). Everything else —
// d2 entirely, and d1 outside the hwy9 corridor — must never appear in the DMZ.
//
// Determinism: the out-of-scope messages are published BEFORE the in-scope
// one. The DMZ stream has a single source over central, whose sourcing
// consumer walks central in sequence order, so once the (later) in-scope
// message has arrived in the DMZ, the source has definitively processed and
// FILTERED the earlier out-of-scope messages. The bounded negative fetches
// after that point are confirmation, not the synchronization mechanism —
// no bare sleep stands in for delivery.
func TestDMZ_OutOfScopeNeverCrosses(t *testing.T) {
	core := startServer(t, "core", hubConf(t, "core", "core", -1))
	dmz := startServer(t, "dmz", leafConf(t, "dmz", "dmz", core))
	checkLeafConnected(t, core, dmz)

	_, coreJS := connectJS(t, core, "core")
	_, dmzJS := connectJS(t, dmz, "dmz")

	// Central holds BOTH district spaces; only part of d1 will be shared.
	if _, err := coreJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_CENTRAL",
		Subjects:  []string{"vikasa.exdot.d1.>", "vikasa.exdot.d2.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("add central stream: %v", err)
	}

	// DMZ egress: the single share exports ONLY the hwy9 corridor of d1. The
	// transform's Source doubles as the sourcing filter, so this is the entire
	// egress allowlist — the property under test.
	if _, err := dmzJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_DMZ",
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
		Sources: []*nats.StreamSource{{
			Name:     "VIKASA_EXDOT_CENTRAL",
			External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
			SubjectTransforms: []nats.SubjectTransformConfig{
				{Source: "vikasa.exdot.d1.hwy9.>", Destination: "vikasa.peer.exdot.hwy9.>"},
			},
		}},
	}); err != nil {
		t.Fatalf("add dmz stream: %v", err)
	}
	waitForStream(t, dmzJS, "VIKASA_EXDOT_DMZ")

	// Out-of-scope FIRST (see determinism note above): a whole unshared
	// district, and an unshared corridor inside the shared district.
	if _, err := coreJS.Publish("vikasa.exdot.d2.secret.data", []byte("must-not-cross")); err != nil {
		t.Fatalf("publish d2 secret: %v", err)
	}
	if _, err := coreJS.Publish("vikasa.exdot.d1.999.signals", []byte("must-not-cross")); err != nil {
		t.Fatalf("publish d1 unshared: %v", err)
	}
	// In-scope LAST — its arrival proves the source has passed the two above.
	if _, err := coreJS.Publish("vikasa.exdot.d1.hwy9.mm42.flow", []byte("hwy9-flow")); err != nil {
		t.Fatalf("publish hwy9: %v", err)
	}

	// POSITIVE: the in-scope message arrives under its PUBLIC subject. This
	// guards against a vacuous pass — an empty/broken pipeline would also show
	// "no leaks", so we first prove the pipeline is live.
	m := expectMsg(t, dmzJS, "VIKASA_EXDOT_DMZ", "vikasa.peer.exdot.hwy9.mm42.flow", 15*time.Second)
	if string(m.Data) != "hwy9-flow" {
		t.Fatalf("in-scope remap: wrong payload %q", m.Data)
	}
	t.Logf("PIPELINE LIVE: vikasa.exdot.d1.hwy9.mm42.flow -> %s", m.Subject)

	// NEGATIVE (bounded fetch): with delivery latency proven above, a short
	// bounded wait on each forbidden filter must yield ZERO messages.
	for _, forbidden := range []string{
		"vikasa.exdot.d1.>",        // internal form of the shared district
		"vikasa.exdot.d2.>",        // entire unshared district
		"vikasa.exdot.d2.secret.>", // the specific secret we published
	} {
		if got := countMsgs(t, dmzJS, "VIKASA_EXDOT_DMZ", forbidden, 1, 2*time.Second); len(got) != 0 {
			t.Fatalf("ISOLATION BREACH: forbidden filter %q matched %d msg(s) in DMZ stream: %v",
				forbidden, len(got), got)
		}
	}

	// NEGATIVE (stream state): the DMZ stream holds EXACTLY the one exported
	// message, and every stored subject is in a PUBLIC form — no internal
	// vikasa.exdot.d1.> / vikasa.exdot.d2.> subject exists at all.
	info, err := dmzJS.StreamInfo("VIKASA_EXDOT_DMZ", &nats.StreamInfoRequest{SubjectsFilter: ">"})
	if err != nil {
		t.Fatalf("dmz stream info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Fatalf("DMZ stream holds %d msgs, want exactly 1 (the exported hwy9 message); subjects: %v",
			info.State.Msgs, info.State.Subjects)
	}
	for subj := range info.State.Subjects {
		if !strings.HasPrefix(subj, "vikasa.peer.exdot.hwy9.") {
			t.Fatalf("ISOLATION BREACH: DMZ stream stores non-public subject %q", subj)
		}
	}
	t.Logf("ISOLATION OK: DMZ stream = %d msg, subjects %v — nothing internal, nothing unshared.",
		info.State.Msgs, info.State.Subjects)
}

// ---------------------------------------------------------------------------
// TEST 2: full chain cabinet -> regional -> central -> dmz.
// ---------------------------------------------------------------------------

// TestDMZ_FullChain stands up the full four-tier leaf hierarchy and proves a
// telemetry message published at a cabinet arrives (a) at central under its
// internal subject and (b) at the DMZ under the remapped public subject.
//
// Leaf hierarchy (remote points leaf -> hub):
//
//	cabinet -> regional(d1a) -> core ; dmz -> core
func TestDMZ_FullChain(t *testing.T) {
	core := startServer(t, "core", hubConf(t, "core", "core", -1))
	// Regional must ALSO accept a leaf (the cabinet), so it is a hub-and-leaf:
	// it solicits up to core AND listens for the cabinet.
	regional := startServer(t, "d1a", hubAndLeafConf(t, "d1a", "d1a", core, -1))
	cabinet := startServer(t, "cab", leafConf(t, "cab", "cab", regional))
	dmz := startServer(t, "dmz", leafConf(t, "dmz", "dmz", core))

	checkLeafConnected(t, core, regional)
	checkLeafConnected(t, regional, cabinet)
	checkLeafConnected(t, core, dmz)

	_, cabJS := connectJS(t, cabinet, "cab")
	_, regJS := connectJS(t, regional, "d1a")
	_, coreJS := connectJS(t, core, "core")
	_, dmzJS := connectJS(t, dmz, "dmz")

	// Cabinet buffer: holds the raw district telemetry.
	if _, err := cabJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_BUFFER",
		Subjects:  []string{"vikasa.exdot.d1.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
	}); err != nil {
		t.Fatalf("add cabinet buffer: %v", err)
	}

	// Regional partition stream sources the cabinet buffer cross-domain.
	if _, err := regJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_D1_D1_0",
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
		Sources: []*nats.StreamSource{{
			Name:     "VIKASA_BUFFER",
			External: &nats.ExternalStream{APIPrefix: "$JS.cab.API"},
		}},
	}); err != nil {
		t.Fatalf("add regional partition: %v", err)
	}

	// Central sources the regional partition cross-domain.
	if _, err := coreJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_CENTRAL",
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
		Sources: []*nats.StreamSource{{
			Name:     "VIKASA_EXDOT_D1_D1_0",
			External: &nats.ExternalStream{APIPrefix: "$JS.d1a.API"},
		}},
	}); err != nil {
		t.Fatalf("add central: %v", err)
	}

	// DMZ sources central cross-domain with the internal->public remap.
	if _, err := dmzJS.AddStream(&nats.StreamConfig{
		Name:      "VIKASA_EXDOT_DMZ",
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
		Sources: []*nats.StreamSource{{
			Name:     "VIKASA_EXDOT_CENTRAL",
			External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
			SubjectTransforms: []nats.SubjectTransformConfig{
				{Source: "vikasa.exdot.d1.>", Destination: "vikasa.exdot.share.research.>"},
			},
		}},
	}); err != nil {
		t.Fatalf("add dmz: %v", err)
	}

	waitForStream(t, regJS, "VIKASA_EXDOT_D1_D1_0")
	waitForStream(t, coreJS, "VIKASA_EXDOT_CENTRAL")
	waitForStream(t, dmzJS, "VIKASA_EXDOT_DMZ")

	// Publish at the CABINET, under the district prefix.
	if _, err := cabJS.Publish("vikasa.exdot.d1.001.signals.1", []byte("telemetry")); err != nil {
		t.Fatalf("cabinet publish: %v", err)
	}

	// (a) Arrives at central under the INTERNAL subject.
	mc := expectMsg(t, coreJS, "VIKASA_EXDOT_CENTRAL", "vikasa.exdot.d1.001.signals.1", 20*time.Second)
	if string(mc.Data) != "telemetry" {
		t.Fatalf("central: wrong payload %q", mc.Data)
	}
	t.Logf("CENTRAL OK: %s", mc.Subject)

	// (b) Arrives at DMZ under the PUBLIC remapped subject.
	md := expectMsg(t, dmzJS, "VIKASA_EXDOT_DMZ", "vikasa.exdot.share.research.001.signals.1", 20*time.Second)
	if string(md.Data) != "telemetry" {
		t.Fatalf("dmz: wrong payload %q", md.Data)
	}
	t.Logf("DMZ REMAP OK: %s", md.Subject)
}

// hubAndLeafConf renders a server that BOTH solicits a leaf up to `hub` (for
// APP and SYS) AND listens for downstream leaf connections on leafListen. Used
// for the regional tier, which is a leaf of core but a hub for the cabinet.
func hubAndLeafConf(t *testing.T, name, domain string, hub *srv, leafListen int) string {
	hp := hub.opts.LeafNode.Port
	return fmt.Sprintf(`
server_name: %s
listen: 127.0.0.1:-1
jetstream { domain: %q, store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
leafnodes {
  listen: 127.0.0.1:%d
  remotes: [
    { url: "nats-leaf://app:app@127.0.0.1:%d", account: APP }
    { url: "nats-leaf://sys:sys@127.0.0.1:%d", account: SYS }
  ]
}
accounts {
  APP: { jetstream: enabled, users: [ { user: app, password: app } ] }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, name, domain, t.TempDir(), leafListen, hp, hp)
}

// checkLeafConnected waits until both servers report at least one leaf
// connection (best-effort sanity gate before exercising cross-domain JS).
func checkLeafConnected(t *testing.T, a, b *srv) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if a.NumLeafNodes() > 0 && b.NumLeafNodes() > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("leaf not connected: %s(%d) <-> %s(%d)", a.Name(), a.NumLeafNodes(), b.Name(), b.NumLeafNodes())
}

// TestDMZ_RePublishToCoreSubscriber proves Decision D3: with RePublish set on the
// DMZ egress stream, a plain core-NATS subscriber (no JetStream consumer) receives
// the transformed share feed. This is the scalable fan-out tier for third parties.
func TestDMZ_RePublishToCoreSubscriber(t *testing.T) {
	core := startServer(t, "core", hubConf(t, "core", "core", -1))
	dmz := startServer(t, "dmz", leafConf(t, "dmz", "dmz", core))
	checkLeafConnected(t, core, dmz)

	_, coreJS := connectJS(t, core, "core")
	if _, err := coreJS.AddStream(&nats.StreamConfig{
		Name:     "VIKASA_EXDOT_CENTRAL_D1_D1_0",
		Storage:  nats.FileStorage,
		Subjects: []string{"vikasa.exdot.d1.>"},
	}); err != nil {
		t.Fatalf("central: %v", err)
	}

	ncDMZ, dmzJS := connectJS(t, dmz, "dmz")
	if _, err := dmzJS.AddStream(&nats.StreamConfig{
		Name:    "VIKASA_EXDOT_DMZ",
		Storage: nats.FileStorage,
		Sources: []*nats.StreamSource{{
			Name:     "VIKASA_EXDOT_CENTRAL_D1_D1_0",
			External: &nats.ExternalStream{APIPrefix: "$JS.core.API"},
			SubjectTransforms: []nats.SubjectTransformConfig{
				{Source: "vikasa.exdot.d1.>", Destination: "vikasa.exdot.share.r.>"},
			},
		}},
		// Core-NATS fan-out echo (D3). Identity transform; loop-safe (source-only stream).
		RePublish: &nats.RePublish{Source: "vikasa.>", Destination: "vikasa.>"},
	}); err != nil {
		t.Fatalf("dmz: %v", err)
	}

	// Plain core subscription on the DMZ server — NOT a JetStream consumer.
	sub, err := ncDMZ.SubscribeSync("vikasa.exdot.share.r.>")
	if err != nil {
		t.Fatalf("core sub: %v", err)
	}
	defer sub.Unsubscribe()
	if err := ncDMZ.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if _, err := coreJS.Publish("vikasa.exdot.d1.001.signals", []byte("sig")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("core subscriber did not receive republished message: %v", err)
	}
	if !strings.HasPrefix(msg.Subject, "vikasa.exdot.share.r.") {
		t.Errorf("republished subject: got %q, want under vikasa.exdot.share.r.", msg.Subject)
	}
	if string(msg.Data) != "sig" {
		t.Errorf("republished payload: got %q, want %q", msg.Data, "sig")
	}
}

// twoAccountJSConf renders a single JetStream server with two accounts wired for
// CROSS-ACCOUNT sourcing (finding C3): ORIGIN exports its JS API as a service plus
// a deliver subject; CONSUMER imports both (API renamed to JS.ORIGIN.API). This is
// the exact account shape the generator must emit for a stream in one account to
// source a stream in another — the piece internal/accounts does NOT model yet.
func twoAccountJSConf(t *testing.T) string {
	return fmt.Sprintf(`
server_name: xacct
listen: 127.0.0.1:-1
jetstream { store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
accounts {
  ORIGIN: {
    jetstream: enabled
    users: [ { user: o, password: o } ]
    exports: [
      { service: "$JS.API.>" }
      { stream: "deliver.origin.>" }
    ]
  }
  CONSUMER: {
    jetstream: enabled
    users: [ { user: c, password: c } ]
    imports: [
      { service: { account: ORIGIN, subject: "$JS.API.>" }, to: "JS.ORIGIN.API.>" }
      { stream: { account: ORIGIN, subject: "deliver.origin.>" } }
    ]
  }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, t.TempDir())
}

// TestCrossAccountSourcing_Reference proves the C3 mechanism: with the $JS.API
// service export/import + deliver-subject import, a stream in CONSUMER can source a
// stream in ORIGIN. This is the working reference for the internal/accounts change.
func TestCrossAccountSourcing_Reference(t *testing.T) {
	s := startServer(t, "xacct", twoAccountJSConf(t))

	// ORIGIN: create a stream and publish.
	ncO, err := nats.Connect(fmt.Sprintf("nats://o:o@127.0.0.1:%d", s.opts.Port))
	if err != nil {
		t.Fatalf("connect ORIGIN: %v", err)
	}
	defer ncO.Close()
	jsO, err := ncO.JetStream()
	if err != nil {
		t.Fatalf("js ORIGIN: %v", err)
	}
	if _, err := jsO.AddStream(&nats.StreamConfig{Name: "ORIGIN_STREAM", Subjects: []string{"data.>"}, Storage: nats.FileStorage}); err != nil {
		t.Fatalf("ORIGIN stream: %v", err)
	}
	if _, err := jsO.Publish("data.1", []byte("hello")); err != nil {
		t.Fatalf("ORIGIN publish: %v", err)
	}

	// CONSUMER: source ORIGIN_STREAM cross-account via the imported API prefix.
	ncC, err := nats.Connect(fmt.Sprintf("nats://c:c@127.0.0.1:%d", s.opts.Port))
	if err != nil {
		t.Fatalf("connect CONSUMER: %v", err)
	}
	defer ncC.Close()
	jsC, err := ncC.JetStream()
	if err != nil {
		t.Fatalf("js CONSUMER: %v", err)
	}
	if _, err := jsC.AddStream(&nats.StreamConfig{
		Name:    "CONSUMER_STREAM",
		Storage: nats.FileStorage,
		Sources: []*nats.StreamSource{{
			Name:     "ORIGIN_STREAM",
			External: &nats.ExternalStream{APIPrefix: "JS.ORIGIN.API", DeliverPrefix: "deliver.origin"},
		}},
	}); err != nil {
		t.Fatalf("CONSUMER stream (cross-account source): %v", err)
	}

	// The sourced message must land in CONSUMER_STREAM.
	deadline := time.Now().Add(5 * time.Second)
	for {
		si, err := jsC.StreamInfo("CONSUMER_STREAM")
		if err == nil && si.State.Msgs >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cross-account source delivered no messages (C3 mechanism failed); last err=%v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestCrossAccountSourcing_FailsWithoutImports is the negative half of the C3
// proof: two JetStream accounts with NO service export/import cannot source across
// the account boundary — the consumer's stream stays empty. This is why the current
// generated account model (stream import only) is insufficient for cross-account
// sourcing, and why internal/accounts must gain a service export/import.
func TestCrossAccountSourcing_FailsWithoutImports(t *testing.T) {
	conf := fmt.Sprintf(`
server_name: xacct2
listen: 127.0.0.1:-1
jetstream { store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
accounts {
  ORIGIN:   { jetstream: enabled, users: [ { user: o, password: o } ] }
  CONSUMER: { jetstream: enabled, users: [ { user: c, password: c } ] }
  SYS:      { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, t.TempDir())
	s := startServer(t, "xacct2", conf)

	ncO, _ := nats.Connect(fmt.Sprintf("nats://o:o@127.0.0.1:%d", s.opts.Port))
	defer ncO.Close()
	jsO, _ := ncO.JetStream()
	if _, err := jsO.AddStream(&nats.StreamConfig{Name: "ORIGIN_STREAM", Subjects: []string{"data.>"}, Storage: nats.FileStorage}); err != nil {
		t.Fatalf("ORIGIN stream: %v", err)
	}
	if _, err := jsO.Publish("data.1", []byte("hello")); err != nil {
		t.Fatalf("ORIGIN publish: %v", err)
	}

	ncC, _ := nats.Connect(fmt.Sprintf("nats://c:c@127.0.0.1:%d", s.opts.Port))
	defer ncC.Close()
	jsC, _ := ncC.JetStream()
	// Same cross-account source config, but the imports don't exist.
	if _, err := jsC.AddStream(&nats.StreamConfig{
		Name:    "CONSUMER_STREAM",
		Storage: nats.FileStorage,
		Sources: []*nats.StreamSource{{
			Name:     "ORIGIN_STREAM",
			External: &nats.ExternalStream{APIPrefix: "JS.ORIGIN.API", DeliverPrefix: "deliver.origin"},
		}},
	}); err != nil {
		t.Fatalf("CONSUMER AddStream: %v", err)
	}

	// Without the service import, nothing can cross: the stream stays empty.
	time.Sleep(2 * time.Second)
	si, err := jsC.StreamInfo("CONSUMER_STREAM")
	if err != nil {
		t.Fatalf("CONSUMER stream info: %v", err)
	}
	if si.State.Msgs != 0 {
		t.Fatalf("expected 0 messages without cross-account imports, got %d — the imports would not be necessary", si.State.Msgs)
	}
}

// TestCrossAccountCrossDomainSourcing is the hardest combination and the real DMZ
// egress shape AFTER the Decision-6 collapse: an INTERNAL account (domain core)
// holds the origin stream; a separate DMZ account (domain dmz, joined to core via a
// per-account leaf) sources it — cross-account AND cross-domain. INTERNAL exports
// its $JS.core.API service + deliver subject; DMZ imports both (same-server on the
// hub) and spans to the dmz server over the leaf. If this is fragile, the DMZ-as-
// separate-account design needs rethinking before any generator/issuance code.
func TestCrossAccountCrossDomainSourcing(t *testing.T) {
	hubConf := fmt.Sprintf(`
server_name: core
listen: 127.0.0.1:-1
jetstream { domain: "core", store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
leafnodes { listen: 127.0.0.1:-1 }
accounts {
  INTERNAL: {
    jetstream: enabled
    users: [ { user: int, password: int } ]
    exports: [
      { service: "$JS.core.API.>" }
      { stream: "deliver.internal.>" }
    ]
  }
  DMZ: {
    jetstream: enabled
    users: [ { user: dmz, password: dmz } ]
    imports: [
      { service: { account: INTERNAL, subject: "$JS.core.API.>" }, to: "JS.INTERNAL.API.>" }
      { stream: { account: INTERNAL, subject: "deliver.internal.>" } }
    ]
  }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, t.TempDir())
	hub := startServer(t, "core", hubConf)

	dmzConf := fmt.Sprintf(`
server_name: dmz
listen: 127.0.0.1:-1
jetstream { domain: "dmz", store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
leafnodes {
  remotes: [
    { url: "nats-leaf://dmz:dmz@127.0.0.1:%d", account: DMZ }
    { url: "nats-leaf://sys:sys@127.0.0.1:%d", account: SYS }
  ]
}
accounts {
  DMZ: { jetstream: enabled, users: [ { user: dmz, password: dmz } ] }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, t.TempDir(), hub.opts.LeafNode.Port, hub.opts.LeafNode.Port)
	dmz := startServer(t, "dmz", dmzConf)
	checkLeafConnected(t, hub, dmz)

	// INTERNAL (on core) hosts the origin stream.
	ncI, err := nats.Connect(fmt.Sprintf("nats://int:int@127.0.0.1:%d", hub.opts.Port))
	if err != nil {
		t.Fatalf("connect INTERNAL: %v", err)
	}
	defer ncI.Close()
	jsI, _ := ncI.JetStream(nats.Domain("core"))
	if _, err := jsI.AddStream(&nats.StreamConfig{Name: "ORIGIN_STREAM", Subjects: []string{"data.>"}, Storage: nats.FileStorage}); err != nil {
		t.Fatalf("INTERNAL stream: %v", err)
	}
	if _, err := jsI.Publish("data.1", []byte("hello")); err != nil {
		t.Fatalf("INTERNAL publish: %v", err)
	}

	// DMZ (on dmz server, joined to core via leaf) sources ORIGIN_STREAM
	// cross-account + cross-domain via the imported API prefix.
	ncD, err := nats.Connect(fmt.Sprintf("nats://dmz:dmz@127.0.0.1:%d", dmz.opts.Port))
	if err != nil {
		t.Fatalf("connect DMZ: %v", err)
	}
	defer ncD.Close()
	jsD, _ := ncD.JetStream(nats.Domain("dmz"))
	if _, err := jsD.AddStream(&nats.StreamConfig{
		Name:    "DMZ_STREAM",
		Storage: nats.FileStorage,
		Sources: []*nats.StreamSource{{
			Name:     "ORIGIN_STREAM",
			External: &nats.ExternalStream{APIPrefix: "JS.INTERNAL.API", DeliverPrefix: "deliver.internal"},
		}},
	}); err != nil {
		t.Fatalf("DMZ stream (cross-account+cross-domain source): %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		si, err := jsD.StreamInfo("DMZ_STREAM")
		if err == nil && si.State.Msgs >= 1 {
			return // success
		}
		if time.Now().After(deadline) {
			t.Fatalf("cross-account+cross-domain source delivered no messages; last err=%v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestCrossAccountCrossDomainSourcing_NoRename records a load-bearing finding: a
// NO-rename service import (importing $JS.core.API as-is, source using the raw
// $JS.core.API prefix) does NOT deliver across the account boundary — the stream
// stays empty. Cross-account sourcing *requires* the import to be renamed
// (to: JS.<origin>.API) and the source to use that renamed prefix (see the passing
// TestCrossAccountCrossDomainSourcing). Consequence for the generator: closing C3 is
// NOT an accounts-model-only change — the Stream CR's externalApiPrefix must become
// the renamed prefix for cross-account sources. This is why the Decision-6 collapse
// (which makes regional→central intra-account, needing no rename) minimizes churn:
// only the DMZ hop stays cross-account.
func TestCrossAccountCrossDomainSourcing_NoRename(t *testing.T) {
	hubConf := fmt.Sprintf(`
server_name: core
listen: 127.0.0.1:-1
jetstream { domain: "core", store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
leafnodes { listen: 127.0.0.1:-1 }
accounts {
  INTERNAL: {
    jetstream: enabled
    users: [ { user: int, password: int } ]
    exports: [
      { service: "$JS.core.API.>" }
      { stream: "deliver.internal.>" }
    ]
  }
  DMZ: {
    jetstream: enabled
    users: [ { user: dmz, password: dmz } ]
    imports: [
      { service: { account: INTERNAL, subject: "$JS.core.API.>" } }
      { stream: { account: INTERNAL, subject: "deliver.internal.>" } }
    ]
  }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, t.TempDir())
	hub := startServer(t, "core", hubConf)

	dmzConf := fmt.Sprintf(`
server_name: dmz
listen: 127.0.0.1:-1
jetstream { domain: "dmz", store_dir: %q, max_mem: 64Mb, max_file: 256Mb }
leafnodes {
  remotes: [
    { url: "nats-leaf://dmz:dmz@127.0.0.1:%d", account: DMZ }
    { url: "nats-leaf://sys:sys@127.0.0.1:%d", account: SYS }
  ]
}
accounts {
  DMZ: { jetstream: enabled, users: [ { user: dmz, password: dmz } ] }
  SYS: { users: [ { user: sys, password: sys } ] }
}
system_account: SYS
`, t.TempDir(), hub.opts.LeafNode.Port, hub.opts.LeafNode.Port)
	dmz := startServer(t, "dmz", dmzConf)
	checkLeafConnected(t, hub, dmz)

	ncI, _ := nats.Connect(fmt.Sprintf("nats://int:int@127.0.0.1:%d", hub.opts.Port))
	defer ncI.Close()
	jsI, _ := ncI.JetStream(nats.Domain("core"))
	if _, err := jsI.AddStream(&nats.StreamConfig{Name: "ORIGIN_STREAM", Subjects: []string{"data.>"}, Storage: nats.FileStorage}); err != nil {
		t.Fatalf("INTERNAL stream: %v", err)
	}
	jsI.Publish("data.1", []byte("hello"))

	ncD, _ := nats.Connect(fmt.Sprintf("nats://dmz:dmz@127.0.0.1:%d", dmz.opts.Port))
	defer ncD.Close()
	jsD, _ := ncD.JetStream(nats.Domain("dmz"))
	// NO rename: the source uses the raw $JS.core.API prefix the generator already emits.
	if _, err := jsD.AddStream(&nats.StreamConfig{
		Name:    "DMZ_STREAM",
		Storage: nats.FileStorage,
		Sources: []*nats.StreamSource{{
			Name:     "ORIGIN_STREAM",
			External: &nats.ExternalStream{APIPrefix: "$JS.core.API", DeliverPrefix: "deliver.internal"},
		}},
	}); err != nil {
		t.Fatalf("DMZ stream (no-rename source): %v", err)
	}

	// Finding: without the rename, nothing crosses — the stream stays empty.
	time.Sleep(3 * time.Second)
	si, err := jsD.StreamInfo("DMZ_STREAM")
	if err != nil {
		t.Fatalf("DMZ stream info: %v", err)
	}
	if si.State.Msgs != 0 {
		t.Fatalf("expected 0 msgs with a no-rename import (rename is required), got %d", si.State.Msgs)
	}
}
