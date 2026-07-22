package credhealth_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/credhealth"
	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/issuance"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// mintBundle mints operator + accounts + one cabinet (so a .creds and a .crt
// exist) into a temp dir and returns it.
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

func find(t *testing.T, recs []credhealth.Record, kind, identity string) credhealth.Record {
	t.Helper()
	for _, r := range recs {
		if r.Kind == kind && r.Identity == identity {
			return r
		}
	}
	t.Fatalf("no record kind=%s identity=%q in %+v", kind, identity, recs)
	return credhealth.Record{}
}

func TestScan_HealthyAtMint(t *testing.T) {
	dir := mintBundle(t)
	rep, err := credhealth.Scan(dir, time.Now(), 720*time.Hour)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !rep.Healthy() || rep.ExitCode() != 0 {
		t.Errorf("expected healthy at mint, got counts %+v exit %d", rep.Counts, rep.ExitCode())
	}
	if c := find(t, rep.Records, "cert", "cab-a"); c.Status != credhealth.StatusOK {
		t.Errorf("leaf cert status = %s, want OK", c.Status)
	}
	if c := find(t, rep.Records, "ca-cert", "vikasa-cabinet-ca"); c.Status != credhealth.StatusOK {
		t.Errorf("ca cert status = %s, want OK", c.Status)
	}
	if u := find(t, rep.Records, "user", "cab-a"); u.Status != credhealth.StatusOK {
		t.Errorf("user jwt status = %s, want OK", u.Status)
	}
	for _, kind := range []string{"operator", "account"} {
		found := false
		for _, r := range rep.Records {
			if r.Kind == kind {
				found = true
				if r.Status != credhealth.StatusNoExpiry {
					t.Errorf("%s %q status = %s, want NO_EXPIRY", kind, r.Identity, r.Status)
				}
			}
		}
		if !found {
			t.Errorf("no %s record found", kind)
		}
	}
}

func TestScan_ExpiringAndExpired(t *testing.T) {
	dir := mintBundle(t)
	base, err := credhealth.Scan(dir, time.Now(), 720*time.Hour)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	cert := find(t, base.Records, "cert", "cab-a")
	if cert.Expiry == nil {
		t.Fatal("cert has no expiry")
	}
	exp := *cert.Expiry

	rep := mustScan(t, dir, exp.Add(-20*24*time.Hour), 30*24*time.Hour)
	if c := find(t, rep.Records, "cert", "cab-a"); c.Status != credhealth.StatusExpiring {
		t.Errorf("status = %s, want EXPIRING", c.Status)
	}
	if rep.Healthy() || rep.ExitCode() != 1 {
		t.Errorf("expected unhealthy (expiring): exit %d counts %+v", rep.ExitCode(), rep.Counts)
	}

	rep2 := mustScan(t, dir, exp.Add(24*time.Hour), 30*24*time.Hour)
	if c := find(t, rep2.Records, "cert", "cab-a"); c.Status != credhealth.StatusExpired {
		t.Errorf("status = %s, want EXPIRED", c.Status)
	}
	if rep2.ExitCode() != 1 {
		t.Errorf("expected exit 1 for expired, got %d", rep2.ExitCode())
	}
}

func TestScan_CorruptCertIsError(t *testing.T) {
	dir := mintBundle(t)
	caPath := filepath.Join(dir, "ca", "cabinet-ca.crt")
	if err := os.WriteFile(caPath, []byte("-----BEGIN CERTIFICATE-----\nnotbase64\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	rep := mustScan(t, dir, time.Now(), 720*time.Hour)
	if c := find(t, rep.Records, "ca-cert", ""); c.Status != credhealth.StatusError {
		t.Errorf("status = %s, want ERROR", c.Status)
	}
	if rep.Healthy() || rep.ExitCode() != 1 {
		t.Error("corrupt cert should make the report unhealthy")
	}
}

func TestScan_MissingDir(t *testing.T) {
	if _, err := credhealth.Scan(filepath.Join(t.TempDir(), "nope"), time.Now(), time.Hour); err == nil {
		t.Error("expected error for missing dir, got nil")
	}
}

func mustScan(t *testing.T, dir string, now time.Time, warn time.Duration) *credhealth.Report {
	t.Helper()
	rep, err := credhealth.Scan(dir, now, warn)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return rep
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
