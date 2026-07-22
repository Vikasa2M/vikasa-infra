package credhealth

import (
	"strings"
	"testing"
	"time"
)

func TestWriteMetrics(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	okExp := now.Add(100 * time.Second)
	expiredExp := now.Add(-50 * time.Second)
	report := &Report{
		Records: []Record{
			{Kind: "cert", Identity: `cab"a`, Path: "cabinets/d7/cab-a.crt", Expiry: &okExp, Status: StatusOK},
			{Kind: "cert", Identity: "cab-b", Path: "cabinets/d7/cab-b.crt", Expiry: &expiredExp, Status: StatusExpired},
			{Kind: "user", Identity: "cab-a", Path: "cabinets/d7/cab-a.creds", Expiry: nil, Status: StatusNoExpiry},
			{Kind: "ca-cert", Identity: "", Path: "ca/cabinet-ca.crt", Status: StatusError, Err: "boom"},
			{Kind: "cert", Identity: "a\\b\nc\"d", Path: "cabinets/d7/cab-c.crt", Expiry: &okExp, Status: StatusOK},
		},
		Counts: map[Status]int{StatusOK: 2, StatusExpired: 1, StatusNoExpiry: 1, StatusError: 1},
	}

	var b strings.Builder
	if err := WriteMetrics(&b, report, now); err != nil {
		t.Fatalf("WriteMetrics: %v", err)
	}
	out := b.String()

	for _, want := range []string{
		"# TYPE vikasa_cred_expiry_seconds gauge",
		`vikasa_cred_expiry_seconds{kind="cert",identity="cab\"a",path="cabinets/d7/cab-a.crt"} 100`,
		`vikasa_cred_expiry_seconds{kind="cert",identity="cab-b",path="cabinets/d7/cab-b.crt"} -50`,
		"vikasa_cred_expiry_seconds{kind=\"cert\",identity=\"a\\\\b\\nc\\\"d\",path=\"cabinets/d7/cab-c.crt\"} 100",
		`vikasa_cred_artifacts_total{status="OK"} 2`,
		`vikasa_cred_artifacts_total{status="EXPIRED"} 1`,
		`vikasa_cred_artifacts_total{status="NO_EXPIRY"} 1`,
		`vikasa_cred_artifacts_total{status="ERROR"} 1`,
		"vikasa_credhealth_last_scan_timestamp_seconds 1700000000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing line %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `path="cabinets/d7/cab-a.creds"`) {
		t.Errorf("NO_EXPIRY artifact must not emit an expiry_seconds series:\n%s", out)
	}
	if strings.Contains(out, `path="ca/cabinet-ca.crt"`) {
		t.Errorf("ERROR artifact must not emit an expiry_seconds series:\n%s", out)
	}
}
