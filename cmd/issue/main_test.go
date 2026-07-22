package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	jwt "github.com/nats-io/jwt/v2"
)

func TestRun_MintsTrustChain(t *testing.T) {
	tmp := credsDir(t)
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, f := range []string{
		"operator.jwt", "operator.nkey", "operator-sk.nkey", "resolver.conf", "accounts.index",
		"accounts/DISTRICT_D7.nkey", "accounts/CENTRAL.nkey", "accounts/SYSTEM.nkey",
	} {
		if _, err := os.Stat(filepath.Join(tmp, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(tmp, "resolver"))
	if err != nil {
		t.Fatalf("read resolver dir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("resolver/ has %d account JWTs, want 3", len(entries))
	}
	// Re-run is a successful no-op for keys.
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}

func TestRun_InvalidSpec(t *testing.T) {
	if err := run(issueOptions{spec: "../../examples/INVALID-orphan.json", out: t.TempDir()}); err == nil {
		t.Error("expected error for invalid spec, got nil")
	}
}

func TestRun_MintsCabinetCreds(t *testing.T) {
	tmp := credsDir(t)
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: "../../examples/exdot-cabinets-scoped.json"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, f := range []string{
		"cabinets/d7/exdot-d7a-cab-001.creds",
		"cabinets/d7/exdot-d7a-cab-001.nkey",
		"cabinets/d7/exdot-d7b-cab-050.creds",
	} {
		if _, err := os.Stat(filepath.Join(tmp, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	// Re-run is a successful no-op for keys.
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: "../../examples/exdot-cabinets-scoped.json"}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}

func TestRun_MintsCabinetClientCerts(t *testing.T) {
	tmp := credsDir(t)
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: "../../examples/exdot-cabinets-scoped.json"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, f := range []string{
		"ca/cabinet-ca.crt",
		"ca/cabinet-ca.key",
		"cabinets/d7/exdot-d7a-cab-001.crt",
		"cabinets/d7/exdot-d7a-cab-001.key",
		"cabinets/d7/exdot-d7b-cab-050.crt",
	} {
		if _, err := os.Stat(filepath.Join(tmp, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	// Re-run is a successful no-op for keys.
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: "../../examples/exdot-cabinets-scoped.json"}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
}

func TestRun_RotateProducesNewCredsAndRetiredLog(t *testing.T) {
	tmp := credsDir(t)
	spec := "../../examples/exdot-shared.json"
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: spec, out: tmp, cabinets: cabs}); err != nil {
		t.Fatalf("initial: %v", err)
	}
	nkeyPath := filepath.Join(tmp, "cabinets", "d7", "exdot-d7a-cab-001.nkey")
	before, err := os.ReadFile(nkeyPath)
	if err != nil {
		t.Fatalf("read nkey: %v", err)
	}
	if err := run(issueOptions{spec: spec, out: tmp, cabinets: cabs, rotate: "exdot-d7a-cab-001"}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	after, err := os.ReadFile(nkeyPath)
	if err != nil {
		t.Fatalf("read nkey: %v", err)
	}
	if string(before) == string(after) {
		t.Error("nkey unchanged after rotation (should be new)")
	}
	if _, err := os.Stat(filepath.Join(tmp, "revocations", "retired.jsonl")); err != nil {
		t.Errorf("retired.jsonl not written: %v", err)
	}
}

func TestRun_RotateOperatorSK(t *testing.T) {
	tmp := credsDir(t)
	spec := "../../examples/exdot-shared.json"
	if err := run(issueOptions{spec: spec, out: tmp}); err != nil {
		t.Fatalf("initial issue: %v", err)
	}
	if err := run(issueOptions{spec: spec, out: tmp, rotateOperatorSK: true}); err != nil {
		t.Fatalf("rotate-operator-sk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "revocations", "retired-operator-sk.jsonl")); err != nil {
		t.Errorf("expected retired-operator-sk.jsonl after rotation: %v", err)
	}
}

func TestRun_RotateWithoutCabinetsErrors(t *testing.T) {
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: t.TempDir(), rotate: "cab-a"}); err == nil {
		t.Error("expected error: -rotate requires -cabinets")
	}
}

func TestSelectExpiring_WideWindowSelectsAll(t *testing.T) {
	tmp := credsDir(t)
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	inv, err := fleet.Load(cabs)
	if err != nil {
		t.Fatalf("load inv: %v", err)
	}
	selected, skipped, err := selectExpiring(tmp, 100*24*time.Hour, time.Now(), inv)
	if err != nil {
		t.Fatalf("selectExpiring: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none (all in inv)", skipped)
	}
	if len(selected) != len(inv.Cabinets) {
		t.Errorf("selected %d cabinets, want all %d: %v", len(selected), len(inv.Cabinets), selected)
	}
}

func TestSelectExpiring_NarrowWindowSelectsNone(t *testing.T) {
	tmp := credsDir(t)
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	inv, _ := fleet.Load(cabs)
	selected, _, err := selectExpiring(tmp, time.Hour, time.Now(), inv)
	if err != nil {
		t.Fatalf("selectExpiring: %v", err)
	}
	if len(selected) != 0 {
		t.Errorf("narrow window selected %v, want none", selected)
	}
}

func TestSelectExpiring_OrphanSkipped(t *testing.T) {
	tmp := credsDir(t)
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	full, _ := fleet.Load(cabs)
	if len(full.Cabinets) < 2 {
		t.Skip("need >=2 cabinets to test orphan")
	}
	orphanID := full.Cabinets[0].ID
	trimmed := &fleet.Inventory{DOT: full.DOT, Cabinets: full.Cabinets[1:]} // drop the first
	selected, skipped, err := selectExpiring(tmp, 100*24*time.Hour, time.Now(), trimmed)
	if err != nil {
		t.Fatalf("selectExpiring: %v", err)
	}
	foundSkipped := false
	for _, id := range skipped {
		if id == orphanID {
			foundSkipped = true
		}
	}
	if !foundSkipped {
		t.Errorf("orphan %q not in skipped %v", orphanID, skipped)
	}
	for _, id := range selected {
		if id == orphanID {
			t.Errorf("orphan %q wrongly selected", orphanID)
		}
	}
}

func userPubKey(t *testing.T, credsPath string) string {
	t.Helper()
	b, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("read creds: %v", err)
	}
	tok, err := jwt.ParseDecoratedJWT(b)
	if err != nil {
		t.Fatalf("parse jwt: %v", err)
	}
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return uc.Subject
}

// credsPathFor finds the minted .creds for a cabinet id under out/cabinets/<district>/.
// The bundle sharding is by district; rather than re-derive it, glob for the file.
func credsPathFor(t *testing.T, out, id string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(out, "cabinets", "*", id+".creds"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("locate creds for %q: matches=%v err=%v", id, matches, err)
	}
	return matches[0]
}

func TestRun_RotateExpiringDryRunDoesNotMutate(t *testing.T) {
	tmp := credsDir(t)
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	inv, _ := fleet.Load(cabs)
	id := inv.Cabinets[0].ID
	credsPath := credsPathFor(t, tmp, id)
	before := userPubKey(t, credsPath)

	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs, rotateExpiring: 100 * 24 * time.Hour}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if after := userPubKey(t, credsPath); after != before {
		t.Errorf("dry-run mutated cred: pubkey changed %s -> %s", before, after)
	}
	if _, err := os.Stat(filepath.Join(tmp, "revocations", "retired.jsonl")); err == nil {
		t.Errorf("dry-run wrote retired.jsonl; must not mutate")
	}
}

func TestRun_RotateExpiringApplyRotates(t *testing.T) {
	tmp := credsDir(t)
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	inv, _ := fleet.Load(cabs)
	id := inv.Cabinets[0].ID
	credsPath := credsPathFor(t, tmp, id)
	before := userPubKey(t, credsPath)

	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs, rotateExpiring: 100 * 24 * time.Hour, apply: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if after := userPubKey(t, credsPath); after == before {
		t.Errorf("apply did not rotate cred: pubkey unchanged %s", before)
	}
	if _, err := os.Stat(filepath.Join(tmp, "revocations", "retired.jsonl")); err != nil {
		t.Errorf("apply did not write retired.jsonl: %v", err)
	}
}

func TestRun_RotateExpiringRequiresCabinets(t *testing.T) {
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: t.TempDir(), rotateExpiring: 100 * 24 * time.Hour, apply: true}); err == nil {
		t.Error("expected error: -rotate-expiring requires -cabinets")
	}
}

func TestRun_ApplyWithoutRotateExpiringErrors(t *testing.T) {
	tmp := credsDir(t)
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs, apply: true}); err == nil {
		t.Error("expected error: --apply without -rotate-expiring")
	}
}

func TestRun_SummaryReportsDMZConsumerCount(t *testing.T) {
	tmp := credsDir(t)
	out, err := captureStdout(t, func() error {
		return run(issueOptions{spec: "../../examples/exdot-dmz.json", out: tmp})
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "dmz-consumers=2") {
		t.Errorf("summary missing dmz-consumers=2 (research-aggregate, peer-neighbor-corridor): %q", out)
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestRun_RotateAccountSK_NoCabinetsWarnsButProceeds(t *testing.T) {
	tmp := credsDir(t)
	spec := "../../examples/exdot-shared.json"
	// First plain issue so there is a prior account JWT to retire.
	if err := run(issueOptions{spec: spec, out: tmp}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	out, err := captureStdout(t, func() error {
		return run(issueOptions{spec: spec, out: tmp, rotateAccountSK: "DISTRICT_D7"})
	})
	if err != nil {
		t.Fatalf("rotate-account-sk without -cabinets should proceed: %v", err)
	}
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "-cabinets") {
		t.Errorf("expected a WARNING about -cabinets, got: %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(tmp, "revocations", "retired-account-sk.jsonl")); statErr != nil {
		t.Errorf("rotation did not proceed (no audit log): %v", statErr)
	}
}

func TestRun_RotateAccountSK_UnknownNameFails(t *testing.T) {
	tmp := credsDir(t)
	err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, rotateAccountSK: "DISTRICT_NOPE"})
	if err == nil || !strings.Contains(err.Error(), "DISTRICT_NOPE") {
		t.Fatalf("expected error naming DISTRICT_NOPE, got %v", err)
	}
}

// Union: a narrow window selects no expiring cabinets, so only the explicit
// -rotate id should rotate (proving union(explicit, selected) carries explicit ids).
func TestRun_RotateExpiringUnionWithExplicit(t *testing.T) {
	tmp := credsDir(t)
	cabs := "../../examples/exdot-cabinets-scoped.json"
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	inv, _ := fleet.Load(cabs)
	if len(inv.Cabinets) < 2 {
		t.Skip("need >=2 cabinets")
	}
	target := inv.Cabinets[0].ID  // explicitly rotated
	control := inv.Cabinets[1].ID // must NOT rotate (narrow window selects none)
	targetPath := credsPathFor(t, tmp, target)
	controlPath := credsPathFor(t, tmp, control)
	tBefore := userPubKey(t, targetPath)
	cBefore := userPubKey(t, controlPath)

	// Narrow window (no expiring) + explicit -rotate target + --apply.
	if err := run(issueOptions{spec: "../../examples/exdot-shared.json", out: tmp, cabinets: cabs, rotate: target, rotateExpiring: time.Hour, apply: true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if userPubKey(t, targetPath) == tBefore {
		t.Errorf("explicit -rotate %q did not rotate (union dropped explicit ids)", target)
	}
	if userPubKey(t, controlPath) != cBefore {
		t.Errorf("control %q rotated but should not have (narrow window, not named)", control)
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
