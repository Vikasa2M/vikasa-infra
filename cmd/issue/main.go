// Command issue mints the NATS operator + account trust chain from a topology
// spec (via the B1 account model) into a local, gitignored credentials dir.
// Unlike cmd/gen this is non-deterministic and produces secrets.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
	"github.com/Vikasa2M/vikasa-infra/internal/credhealth"
	"github.com/Vikasa2M/vikasa-infra/internal/fleet"
	"github.com/Vikasa2M/vikasa-infra/internal/issuance"
	"github.com/Vikasa2M/vikasa-infra/internal/topology"
)

// issueOptions holds the cmd/issue flag values passed to run().
type issueOptions struct {
	spec             string
	out              string
	cabinets         string
	rotate           string
	rotateExpiring   time.Duration
	apply            bool
	rotateOperatorSK bool
	rotateAccountSK  string
}

func main() {
	var o issueOptions
	flag.StringVar(&o.spec, "spec", "examples/exdot-shared.json", "path to topology spec (RFC 7951 JSON)")
	flag.StringVar(&o.out, "out", "creds", "output directory for minted credentials (gitignored)")
	flag.StringVar(&o.cabinets, "cabinets", "", "optional cabinet inventory JSON; mints per-cabinet user creds")
	flag.StringVar(&o.rotate, "rotate", "", "comma-separated cabinet ids to rotate (re-key); requires -cabinets")
	flag.DurationVar(&o.rotateExpiring, "rotate-expiring", 0, "rotate cabinets whose creds expire within this window (requires -cabinets); dry-run unless --apply")
	flag.BoolVar(&o.apply, "apply", false, "with -rotate-expiring, actually rotate (default: dry-run preview only)")
	flag.BoolVar(&o.rotateOperatorSK, "rotate-operator-sk", false, "force-rotate the operator signing key (immediate swap): re-key operator-sk.nkey, re-sign all accounts, retire the old SK to revocations/retired-operator-sk.jsonl")
	flag.StringVar(&o.rotateAccountSK, "rotate-account-sk", "", "comma-separated account names whose signing key to force-rotate (immediate swap): re-key accounts/<NAME>-sk.nkey, re-sign that account's user JWTs, retire the old SK to revocations/retired-account-sk.jsonl")
	flag.Parse()

	if err := run(o); err != nil {
		fmt.Fprintln(os.Stderr, "issue:", err)
		os.Exit(1)
	}
}

func run(o issueOptions) error {
	root, err := topology.Load(o.spec)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	model, err := accounts.Build(root)
	if err != nil {
		return fmt.Errorf("build accounts: %w", err)
	}
	var inv *fleet.Inventory
	if o.cabinets != "" {
		inv, err = fleet.Load(o.cabinets)
		if err != nil {
			return fmt.Errorf("load cabinets: %w", err)
		}
	}

	var rotateIDs []string
	for id := range strings.SplitSeq(o.rotate, ",") {
		if id = strings.TrimSpace(id); id != "" {
			rotateIDs = append(rotateIDs, id)
		}
	}
	var rotateAccountSKNames []string
	for name := range strings.SplitSeq(o.rotateAccountSK, ",") {
		if name = strings.TrimSpace(name); name != "" {
			rotateAccountSKNames = append(rotateAccountSKNames, name)
		}
	}
	if len(rotateAccountSKNames) > 0 && o.cabinets == "" {
		fmt.Println("issue: WARNING -rotate-account-sk without -cabinets: the named accounts' user creds will be rejected until re-issued with -cabinets")
	}
	if (len(rotateIDs) > 0 || o.rotateExpiring > 0) && o.cabinets == "" {
		return fmt.Errorf("-rotate / -rotate-expiring requires -cabinets")
	}
	if o.apply && o.rotateExpiring == 0 {
		return fmt.Errorf("--apply has no effect without -rotate-expiring")
	}

	if o.rotateExpiring > 0 {
		selected, skipped, serr := selectExpiring(o.out, o.rotateExpiring, time.Now(), inv)
		if serr != nil {
			return fmt.Errorf("select expiring: %w", serr)
		}
		for _, id := range skipped {
			fmt.Printf("issue: WARNING %q has an expiring cred but is not in -cabinets; skipping\n", id)
		}
		if !o.apply {
			fmt.Printf("issue: -rotate-expiring dry-run (window=%s): would rotate %d cabinet(s): %s\n  re-run with --apply to rotate\n",
				o.rotateExpiring, len(selected), strings.Join(selected, ","))
			return nil
		}
		rotateIDs = unionStrings(rotateIDs, selected)
	}

	res, err := issuance.IssueWithRotation(model, inv, root, o.out, issuance.RotationSpec{
		Cabinets:   rotateIDs,
		OperatorSK: o.rotateOperatorSK,
		AccountSK:  rotateAccountSKNames,
	})
	if err != nil {
		return fmt.Errorf("issue: %w", err)
	}

	minted, reused := 0, 0
	skMinted, skReused, skRotated := 0, 0, 0
	for _, a := range res.Accounts {
		if a.Minted {
			minted++
		} else {
			reused++
		}
		if a.SigningMinted {
			skMinted++
		} else {
			skReused++
		}
		if a.SigningRotated {
			skRotated++
		}
	}
	cabMinted, cabReused := 0, 0
	certMinted, certReused := 0, 0
	for _, c := range res.Cabinets {
		if c.Minted {
			cabMinted++
		} else {
			cabReused++
		}
		if c.CertMinted {
			certMinted++
		} else {
			certReused++
		}
	}
	fmt.Printf("issue: DOT=%s  operator=%s (minted=%t)  op-sk=%s (minted=%t rotated=%t)  accounts=%d (minted=%d reused=%d)  acct-sk=%d (minted=%d reused=%d rotated=%d)  cabinets=%d (minted=%d reused=%d)  certs=%d (minted=%d reused=%d ca-created=%t)  rotated=%d  revoked=%d  dmz-consumers=%d  out=%s\n",
		model.DOT, res.OperatorPub, res.OperatorMinted, res.OperatorSigningPub, res.OperatorSigningMinted, res.OperatorSigningRotated,
		len(res.Accounts), minted, reused,
		len(res.Accounts), skMinted, skReused, skRotated,
		len(res.Cabinets), cabMinted, cabReused,
		len(res.Cabinets), certMinted, certReused, res.CACreated,
		len(res.Rotated), res.Revoked, len(res.DMZConsumers), o.out)
	return nil
}

// unionStrings merges two id slices, de-duplicated and sorted.
func unionStrings(a, b []string) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		set[s] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// selectExpiring scans the bundle in out and returns the cabinet ids whose
// per-cabinet credentials (user JWT or client cert) expire within window (or are
// already expired), split into those present in inv (selected, eligible to
// rotate) and those absent from inv (skipped — likely decommissioned). Both
// slices are de-duplicated and sorted.
func selectExpiring(out string, window time.Duration, now time.Time, inv *fleet.Inventory) (selected, skipped []string, err error) {
	report, err := credhealth.Scan(out, now, window)
	if err != nil {
		return nil, nil, err
	}
	inInv := make(map[string]struct{})
	if inv != nil {
		for _, c := range inv.Cabinets {
			inInv[c.ID] = struct{}{}
		}
	}
	seen := make(map[string]struct{})
	for _, r := range report.Records {
		if r.Kind != "user" && r.Kind != "cert" {
			continue
		}
		if r.Status != credhealth.StatusExpiring && r.Status != credhealth.StatusExpired {
			continue
		}
		if r.Identity == "" {
			continue
		}
		if _, dup := seen[r.Identity]; dup {
			continue
		}
		seen[r.Identity] = struct{}{}
		if _, ok := inInv[r.Identity]; ok {
			selected = append(selected, r.Identity)
		} else {
			skipped = append(skipped, r.Identity)
		}
	}
	sort.Strings(selected)
	sort.Strings(skipped)
	return selected, skipped, nil
}
