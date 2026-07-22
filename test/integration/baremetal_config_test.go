//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/render"
)

// TestDMZ_BareMetalRenderedConfig proves the bare-metal renderer's stream JSON
// is a valid `nats stream add --config` input for the DMZ egress stream: the
// rendered bytes (not a hand-written copy) are unmarshalled into
// nats.StreamConfig — the same decode the NATS CLI performs — and created on a
// live DMZ-domain server sourcing central cross-domain. Messages published
// under the internal district space must arrive ONLY under the public share
// spaces, exactly as on the kubernetes/NACK path.
func TestDMZ_BareMetalRenderedConfig(t *testing.T) {
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

	// Render the DMZ cluster with the REAL bare-metal renderer. Replicas 1
	// because the embedded DMZ "cluster" is a single server.
	files, err := render.BareMetalRenderer{}.RenderCluster(render.ClusterSlice{
		ID:                  "dmz",
		SubstrateType:       "bare-metal",
		Hosts:               []string{"exdot-dmz-1"},
		DOT:                 "exdot",
		JSDomain:            "dmz",
		LeafEndpoint:        "leaf-dmz.nats.vikasa.exdot:7422",
		CentralLeafEndpoint: "leaf-core.nats.vikasa.exdot:7422",
		Streams: []plan.Stream{{
			Name:     "VIKASA_EXDOT_DMZ",
			Cluster:  "dmz",
			JSDomain: "dmz",
			Replicas: 1,
			Tier:     "dmz",

			Sources: []plan.Source{
				{
					Name:            "VIKASA_EXDOT_CENTRAL",
					Domain:          "core",
					TransformSource: "vikasa.exdot.d1.>",
					TransformDest:   "vikasa.exdot.share.research.>",
				},
				{
					Name:            "VIKASA_EXDOT_CENTRAL",
					Domain:          "core",
					TransformSource: "vikasa.exdot.d1.hwy9.>",
					TransformDest:   "vikasa.peer.exdot.hwy9.>",
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("bare-metal RenderCluster(dmz): %v", err)
	}
	raw, ok := files["streams/VIKASA_EXDOT_DMZ.json"]
	if !ok {
		t.Fatal("renderer did not produce streams/VIKASA_EXDOT_DMZ.json")
	}

	var cfg nats.StreamConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("rendered stream JSON does not decode as StreamConfig: %v\n%s", err, raw)
	}
	if _, err := dmzJS.AddStream(&cfg); err != nil {
		t.Fatalf("server rejected the rendered bare-metal stream config: %v\n%s", err, raw)
	}
	waitForStream(t, dmzJS, "VIKASA_EXDOT_DMZ")

	if _, err := coreJS.Publish("vikasa.exdot.d1.001.signals", []byte("sig-001")); err != nil {
		t.Fatalf("publish d1.001: %v", err)
	}
	if _, err := coreJS.Publish("vikasa.exdot.d1.hwy9.mm42.flow", []byte("hwy9-flow")); err != nil {
		t.Fatalf("publish d1.hwy9: %v", err)
	}

	m := expectMsg(t, dmzJS, "VIKASA_EXDOT_DMZ", "vikasa.exdot.share.research.001.signals", 15*time.Second)
	if string(m.Data) != "sig-001" {
		t.Fatalf("research remap: wrong payload %q", m.Data)
	}
	peer := expectMsg(t, dmzJS, "VIKASA_EXDOT_DMZ", "vikasa.peer.exdot.hwy9.mm42.flow", 15*time.Second)
	if string(peer.Data) != "hwy9-flow" {
		t.Fatalf("peer remap: wrong payload %q", peer.Data)
	}
	t.Logf("BARE-METAL CONFIG OK: research=%s peer=%s", m.Subject, peer.Subject)
}
