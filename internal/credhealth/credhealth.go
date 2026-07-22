// Package credhealth scans a minted credential bundle and classifies each
// time-bound artifact (JWTs + X.509 certs) by expiry. It is read-only and
// touches only public material — JWT claims and cert metadata; it extracts the
// JWT out of a decorated .creds and ignores the embedded seed, and never opens
// .nkey/.key files.
package credhealth

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
)

// Status is the expiry classification of one credential artifact.
type Status string

const (
	StatusOK       Status = "OK"
	StatusExpiring Status = "EXPIRING"
	StatusExpired  Status = "EXPIRED"
	StatusNoExpiry Status = "NO_EXPIRY"
	StatusError    Status = "ERROR"
)

// Record is one scanned credential artifact.
type Record struct {
	Kind     string     // operator | account | user | cert | ca-cert
	Identity string     // claims Name / cert CN (empty if unparseable)
	Path     string     // path relative to the scanned dir (slash-separated)
	Issued   time.Time  // JWT iat or cert NotBefore (zero if unknown)
	Expiry   *time.Time // nil = no expiry (eternal JWT)
	Status   Status
	Err      string // populated when Status == StatusError
}

// Report is the result of a Scan.
type Report struct {
	Records []Record // sorted by (Kind, Identity, Path)
	Counts  map[Status]int
}

// Healthy reports whether nothing needs attention (no EXPIRED/EXPIRING/ERROR).
// NO_EXPIRY alone is healthy.
func (r *Report) Healthy() bool {
	return r.Counts[StatusExpired] == 0 && r.Counts[StatusExpiring] == 0 && r.Counts[StatusError] == 0
}

// ExitCode is 0 when Healthy, else 1.
func (r *Report) ExitCode() int {
	if r.Healthy() {
		return 0
	}
	return 1
}

// Scan walks dir's known layout and classifies each time-bound artifact against
// now and the warn window. A missing/!dir path is a hard error; an unparseable
// known artifact becomes an ERROR record rather than failing the whole scan.
func Scan(dir string, now time.Time, warn time.Duration) (*Report, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("credhealth: scan %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("credhealth: %s is not a directory", dir)
	}

	var records []Record
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// WalkDir delivers werr for entries it cannot stat — a structural
			// problem with the tree, so abort and let Scan surface it.
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rec, ok := recordFor(dir, filepath.ToSlash(rel), now, warn); ok {
			records = append(records, rec)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("credhealth: walk %s: %w", dir, walkErr)
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Kind != records[j].Kind {
			return records[i].Kind < records[j].Kind
		}
		if records[i].Identity != records[j].Identity {
			return records[i].Identity < records[j].Identity
		}
		return records[i].Path < records[j].Path
	})

	counts := map[Status]int{}
	for _, r := range records {
		counts[r.Status]++
	}
	return &Report{Records: records, Counts: counts}, nil
}

// recordFor builds a Record for a known artifact path (relative, slash-separated).
// The second return is false for files that are not time-bound artifacts.
func recordFor(dir, rel string, now time.Time, warn time.Duration) (Record, bool) {
	full := filepath.Join(dir, filepath.FromSlash(rel))
	switch {
	case rel == "operator.jwt":
		return jwtRecord(full, rel, "operator", now, warn), true
	case strings.HasPrefix(rel, "resolver/") && strings.HasSuffix(rel, ".jwt"):
		return jwtRecord(full, rel, "account", now, warn), true
	case rel == "ca/cabinet-ca.crt":
		return certRecord(full, rel, "ca-cert", now, warn), true
	case strings.HasPrefix(rel, "cabinets/") && strings.HasSuffix(rel, ".creds"):
		return jwtRecord(full, rel, "user", now, warn), true
	case strings.HasPrefix(rel, "cabinets/") && strings.HasSuffix(rel, ".crt"):
		return certRecord(full, rel, "cert", now, warn), true
	default:
		return Record{}, false
	}
}

// jwtRecord decodes a JWT artifact's claims. ParseDecoratedJWT handles both
// bare JWTs (operator.jwt, resolver/*.jwt) and decorated .creds files (JWT +
// seed); for bare JWTs it returns the content unchanged, so no separate code
// path is needed. TrimSpace preserves tolerance for trailing whitespace.
func jwtRecord(full, rel, kind string, now time.Time, warn time.Duration) Record {
	rec := Record{Kind: kind, Path: rel}
	data, err := os.ReadFile(full)
	if err != nil {
		rec.Status, rec.Err = StatusError, err.Error()
		return rec
	}
	token, perr := jwt.ParseDecoratedJWT(data)
	if perr != nil {
		rec.Status, rec.Err = StatusError, perr.Error()
		return rec
	}
	token = strings.TrimSpace(token)
	claims, derr := jwt.Decode(token)
	if derr != nil {
		rec.Status, rec.Err = StatusError, derr.Error()
		return rec
	}
	cd := claims.Claims()
	rec.Identity = cd.Name
	if rec.Identity == "" {
		rec.Identity = cd.Subject
	}
	if cd.IssuedAt != 0 {
		rec.Issued = time.Unix(cd.IssuedAt, 0).UTC()
	}
	if cd.Expires != 0 {
		exp := time.Unix(cd.Expires, 0).UTC()
		rec.Expiry = &exp
	}
	rec.Status = classify(rec.Expiry, now, warn)
	return rec
}

// certRecord parses an X.509 cert artifact's metadata.
func certRecord(full, rel, kind string, now time.Time, warn time.Duration) Record {
	rec := Record{Kind: kind, Path: rel}
	data, err := os.ReadFile(full)
	if err != nil {
		rec.Status, rec.Err = StatusError, err.Error()
		return rec
	}
	blk, _ := pem.Decode(data)
	if blk == nil || blk.Type != "CERTIFICATE" {
		rec.Status, rec.Err = StatusError, "invalid certificate PEM"
		return rec
	}
	cert, perr := x509.ParseCertificate(blk.Bytes)
	if perr != nil {
		rec.Status, rec.Err = StatusError, perr.Error()
		return rec
	}
	rec.Identity = cert.Subject.CommonName
	rec.Issued = cert.NotBefore.UTC()
	exp := cert.NotAfter.UTC()
	rec.Expiry = &exp
	rec.Status = classify(rec.Expiry, now, warn)
	return rec
}

// classify maps an expiry against now and the warn window. A nil expiry (a JWT
// with no exp) is NO_EXPIRY.
func classify(expiry *time.Time, now time.Time, warn time.Duration) Status {
	if expiry == nil {
		return StatusNoExpiry
	}
	if !now.Before(*expiry) { // now >= expiry: on or after exp is EXPIRED (RFC 7519 §4.1.4)
		return StatusExpired
	}
	if expiry.Sub(now) <= warn {
		return StatusExpiring
	}
	return StatusOK
}
