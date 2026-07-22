package render_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-infra/internal/plan"
	"github.com/Vikasa2M/vikasa-infra/internal/render"
)

func TestBareMetalRenderer_RegionalCluster(t *testing.T) {
	slice := render.ClusterSlice{
		ID:                  "d7a",
		SubstrateType:       "bare-metal",
		Hosts:               []string{"exdot-d7a-1", "exdot-d7a-2", "exdot-d7a-3"},
		JSDomain:            "d7a",
		LeafEndpoint:        "leaf-d7a.nats.vikasa.exdot:7422",
		DOT:                 "exdot",
		IsCentral:           false,
		CentralLeafEndpoint: "leaf-core.nats.vikasa.exdot:7422",
		Streams: []plan.Stream{{
			Name: "VIKASA_EXDOT_D7_D7_0", Cluster: "d7a", JSDomain: "d7a",
			Replicas: 3, MaxAge: "6h", Tier: "regional",
		}},
	}

	files, err := render.BareMetalRenderer{}.RenderCluster(slice)
	if err != nil {
		t.Fatalf("RenderCluster: %v", err)
	}

	for _, name := range []string{
		"nats-exdot-d7a-1.conf", "nats-exdot-d7a-2.conf", "nats-exdot-d7a-3.conf",
		"nats-server.service", "streams/VIKASA_EXDOT_D7_D7_0.json",
	} {
		if _, ok := files[name]; !ok {
			t.Errorf("missing expected file %q", name)
		}
	}

	conf := string(files["nats-exdot-d7a-1.conf"])
	if !strings.Contains(conf, "server_name: d7a-1") {
		t.Errorf("conf: expected server_name d7a-1\n%s", conf)
	}
	if !strings.Contains(conf, "domain: d7a") {
		t.Errorf("conf: expected jetstream domain d7a\n%s", conf)
	}
	if !strings.Contains(conf, "nats://exdot-d7a-2:6222") {
		t.Errorf("conf: expected cluster route to peer\n%s", conf)
	}
	if strings.Contains(conf, "nats://exdot-d7a-1:6222") {
		t.Errorf("conf for d7a-1 must NOT list a route to itself\n%s", conf)
	}
	if !strings.Contains(conf, `urls: ["tls://leaf-core.nats.vikasa.exdot:7422"]`) {
		t.Errorf("regional remote must dial central over tls://\n%s", conf)
	}
	// The outbound leaf remote must present a client cert (mTLS), not just the listener.
	if strings.Count(conf, "leafnode-server-cert.pem") < 2 {
		t.Errorf("expected leafnode cert on BOTH the listener and the remote\n%s", conf)
	}
	if !strings.Contains(conf, `include "security.conf"`) {
		t.Errorf("conf: expected include of B-owned security.conf\n%s", conf)
	}

	for _, want := range []string{
		"cert_file: /etc/vikasa/tls/leafnode-server-cert.pem",
		"cert_file: /etc/vikasa/tls/cluster-cert.pem",
		"cert_file: /etc/vikasa/tls/client-cert.pem",
		"verify: true",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nats.conf missing TLS line %q\n%s", want, conf)
		}
	}

	js := string(files["streams/VIKASA_EXDOT_D7_D7_0.json"])
	if !strings.Contains(js, `"max_age": 21600000000000`) {
		t.Errorf("stream json: expected max_age in ns\n%s", js)
	}
	if !strings.Contains(js, `"num_replicas": 3`) {
		t.Errorf("stream json: expected num_replicas 3\n%s", js)
	}

	checkGolden(t, "baremetal-d7a/nats-exdot-d7a-1.conf", files["nats-exdot-d7a-1.conf"])
	checkGolden(t, "baremetal-d7a/nats-server.service", files["nats-server.service"])
	checkGolden(t, "baremetal-d7a/VIKASA_EXDOT_D7_D7_0.json", files["streams/VIKASA_EXDOT_D7_D7_0.json"])
}

func TestBareMetalRenderer_CentralHasNoLeafRemote(t *testing.T) {
	slice := render.ClusterSlice{
		ID: "core", SubstrateType: "bare-metal", Hosts: []string{"exdot-core-1"},
		JSDomain: "core", DOT: "exdot", IsCentral: true,
		Streams: []plan.Stream{{
			Name: "VIKASA_EXDOT_CENTRAL", Cluster: "core", JSDomain: "core",
			Replicas: 5, Tier: "central",
			Sources: []plan.Source{{Name: "VIKASA_EXDOT_D7_D7_0", Domain: "d7a"}},
		}},
	}
	files, err := render.BareMetalRenderer{}.RenderCluster(slice)
	if err != nil {
		t.Fatalf("RenderCluster: %v", err)
	}
	conf := string(files["nats-exdot-core-1.conf"])
	if strings.Contains(conf, "remotes") {
		t.Errorf("central cluster must NOT have a leaf remote\n%s", conf)
	}
	js := string(files["streams/VIKASA_EXDOT_CENTRAL.json"])
	if !strings.Contains(js, `"api": "$JS.d7a.API"`) {
		t.Errorf("central stream json: expected external api for source\n%s", js)
	}
	if strings.Contains(js, `"max_age"`) {
		t.Errorf("central stream has empty MaxAge → max_age must be omitted\n%s", js)
	}
}

func TestBareMetalRenderer_DMZSourceTransform(t *testing.T) {
	// Same shape as TestK8sRenderer_DMZSourceTransform: a DMZ stream whose
	// per-share sources carry subject transforms. The bare-metal stream JSON
	// must emit subject_transforms (never filter_subject) for those sources.
	dmzStream := plan.Stream{
		Name:     "VIKASA_EXDOT_DMZ",
		Cluster:  "dmz",
		JSDomain: "dmz",
		Replicas: 3,
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
	}

	files, err := render.BareMetalRenderer{}.RenderCluster(render.ClusterSlice{
		ID:                  "dmz",
		SubstrateType:       "bare-metal",
		Hosts:               []string{"exdot-dmz-1"},
		DOT:                 "exdot",
		JSDomain:            "dmz",
		LeafEndpoint:        "leaf-dmz.nats.vikasa.exdot:7422",
		CentralLeafEndpoint: "leaf-core.nats.vikasa.exdot:7422",
		Streams:             []plan.Stream{dmzStream},
	})
	if err != nil {
		t.Fatalf("RenderCluster(dmz): %v", err)
	}
	raw, ok := files["streams/VIKASA_EXDOT_DMZ.json"]
	if !ok {
		t.Fatal("dmz: streams/VIKASA_EXDOT_DMZ.json not produced")
	}

	var cfg struct {
		Sources []struct {
			Name     string `json:"name"`
			External struct {
				API string `json:"api"`
			} `json:"external"`
			FilterSubject     string `json:"filter_subject"`
			SubjectTransforms []struct {
				Src  string `json:"src"`
				Dest string `json:"dest"`
			} `json:"subject_transforms"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("stream json does not parse: %v\n%s", err, raw)
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("want 2 sources, got %d:\n%s", len(cfg.Sources), raw)
	}
	wantTransforms := [][2]string{
		{"vikasa.exdot.d1.>", "vikasa.exdot.share.research.>"},
		{"vikasa.exdot.d1.hwy9.>", "vikasa.peer.exdot.hwy9.>"},
	}
	for i, src := range cfg.Sources {
		if src.Name != "VIKASA_EXDOT_CENTRAL" {
			t.Errorf("source %d: name %q, want VIKASA_EXDOT_CENTRAL", i, src.Name)
		}
		if src.External.API != "$JS.core.API" {
			t.Errorf("source %d: external.api %q, want $JS.core.API", i, src.External.API)
		}
		if src.FilterSubject != "" {
			t.Errorf("source %d: transform sources must not emit filter_subject (%q)", i, src.FilterSubject)
		}
		if len(src.SubjectTransforms) != 1 ||
			src.SubjectTransforms[0].Src != wantTransforms[i][0] ||
			src.SubjectTransforms[0].Dest != wantTransforms[i][1] {
			t.Errorf("source %d: subject_transforms = %+v, want src=%q dest=%q",
				i, src.SubjectTransforms, wantTransforms[i][0], wantTransforms[i][1])
		}
	}
}

func TestBareMetalRenderer_RePublish(t *testing.T) {
	dmz := plan.Stream{
		Name: "VIKASA_EXDOT_DMZ", Replicas: 3, MaxAge: "1h", MaxBytes: 10 << 30, Duplicates: "5m",
		RePublishSource: "vikasa.>", RePublishDest: "vikasa.>",
	}
	files, err := render.BareMetalRenderer{}.RenderCluster(render.ClusterSlice{
		ID: "dmz", SubstrateType: "bare-metal", Hosts: []string{"exdot-dmz-1"},
		DOT: "exdot", JSDomain: "dmz",
		LeafEndpoint: "leaf-dmz.nats.vikasa.exdot:7422", CentralLeafEndpoint: "leaf-core.nats.vikasa.exdot:7422",
		Streams: []plan.Stream{dmz},
	})
	if err != nil {
		t.Fatalf("RenderCluster(dmz): %v", err)
	}
	var cfg struct {
		RePublish *struct {
			Src  string `json:"src"`
			Dest string `json:"dest"`
		} `json:"republish"`
	}
	if err := json.Unmarshal(files["streams/VIKASA_EXDOT_DMZ.json"], &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RePublish == nil || cfg.RePublish.Src != "vikasa.>" || cfg.RePublish.Dest != "vikasa.>" {
		t.Errorf("republish = %+v, want src/dest vikasa.>", cfg.RePublish)
	}
}

func TestBareMetalRenderer_StreamBounds(t *testing.T) {
	dmz := plan.Stream{
		Name: "VIKASA_EXDOT_DMZ", Cluster: "dmz", JSDomain: "dmz", Replicas: 3, Tier: "dmz",
		MaxAge: "1h", MaxBytes: 10 << 30, Duplicates: "5m",
	}
	files, err := render.BareMetalRenderer{}.RenderCluster(render.ClusterSlice{
		ID: "dmz", SubstrateType: "bare-metal", Hosts: []string{"exdot-dmz-1"},
		DOT: "exdot", JSDomain: "dmz",
		LeafEndpoint: "leaf-dmz.nats.vikasa.exdot:7422", CentralLeafEndpoint: "leaf-core.nats.vikasa.exdot:7422",
		Streams: []plan.Stream{dmz},
	})
	if err != nil {
		t.Fatalf("RenderCluster(dmz): %v", err)
	}
	raw, ok := files["streams/VIKASA_EXDOT_DMZ.json"]
	if !ok {
		t.Fatal("dmz: streams/VIKASA_EXDOT_DMZ.json not produced")
	}
	var cfg struct {
		MaxBytes   int64 `json:"max_bytes"`
		Duplicates int64 `json:"duplicates"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("stream json does not parse: %v\n%s", err, raw)
	}
	if cfg.MaxBytes != 10<<30 {
		t.Errorf("max_bytes = %d, want %d", cfg.MaxBytes, int64(10)<<30)
	}
	if cfg.Duplicates != 300000000000 { // 5m in nanoseconds
		t.Errorf("duplicates = %d ns, want 300000000000 (5m)", cfg.Duplicates)
	}
}
