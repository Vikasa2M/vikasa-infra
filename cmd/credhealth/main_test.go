package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/issuance"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

func mintBundle(t *testing.T) string {
	t.Helper()
	dir := credsDir(t)
	root, err := topology.Load("../../examples/exdot-shared.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := accounts.Build(root)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	inv := &fleet.Inventory{DOT: "exdot", Cabinets: []fleet.Cabinet{
		{ID: "cab-a", Partition: "d7/0", Filter: "vikasa.exdot.d7.a.>"},
	}}
	if _, err := issuance.Issue(m, inv, root, dir); err != nil {
		t.Fatalf("issue: %v", err)
	}
	return dir
}

func TestRun_HealthyBundle(t *testing.T) {
	dir := mintBundle(t)
	var buf bytes.Buffer
	code := run(dir, 720*time.Hour, "", time.Now(), &buf, &buf)
	if code != 0 {
		t.Errorf("exit = %d, want 0; output:\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"cab-a", "NO_EXPIRY", "OK", "credhealth:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRun_MissingDir(t *testing.T) {
	var buf bytes.Buffer
	if code := run(filepath.Join(t.TempDir(), "nope"), time.Hour, "", time.Now(), &buf, &buf); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestRun_UnhealthyBundleExits1(t *testing.T) {
	dir := mintBundle(t)
	// Corrupt the CA cert so it scans as an ERROR record.
	caPath := filepath.Join(dir, "ca", "cabinet-ca.crt")
	if err := os.WriteFile(caPath, []byte("-----BEGIN CERTIFICATE-----\nnotbase64\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	var buf bytes.Buffer
	if code := run(dir, 720*time.Hour, "", time.Now(), &buf, &buf); code != 1 {
		t.Errorf("exit = %d, want 1; output:\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "ERROR") {
		t.Errorf("output should contain an ERROR row:\n%s", buf.String())
	}
}

func TestRun_MetricsOutError(t *testing.T) {
	dir := mintBundle(t)
	// Parent directory does not exist → the metrics write must fail and run must exit 1.
	bad := filepath.Join(t.TempDir(), "nope", "credhealth.prom")
	var buf bytes.Buffer
	code := run(dir, 720*time.Hour, bad, time.Now(), &buf, &buf)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (metrics write should fail); output:\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "credhealth: metrics:") {
		t.Errorf("expected a 'credhealth: metrics:' error line:\n%s", buf.String())
	}
}

func TestRun_MetricsOut(t *testing.T) {
	dir := mintBundle(t)
	out := filepath.Join(t.TempDir(), "credhealth.prom")
	var buf bytes.Buffer
	code := run(dir, 720*time.Hour, out, time.Now(), &buf, &buf)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", code, buf.String())
	}
	// Table output is still produced.
	if !strings.Contains(buf.String(), "credhealth:") {
		t.Errorf("table output missing:\n%s", buf.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("metrics file not written: %v", err)
	}
	m := string(data)
	for _, want := range []string{
		"# TYPE vikasa_cred_artifacts_total gauge",
		"vikasa_credhealth_last_scan_timestamp_seconds",
		"vikasa_cred_expiry_seconds{", // the bundle has a CA + leaf cert with expiry
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics file missing %q:\n%s", want, m)
		}
	}
}

// credsDir returns a temp dir tightened to 0700: issuance refuses to write
// secret material into a group/world-accessible tree, and t.TempDir()'s
// per-test subdirectories are created 0755.
func credsDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return d
}
