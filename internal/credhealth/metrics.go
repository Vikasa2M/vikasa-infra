package credhealth

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Exported Prometheus metric names emitted by WriteMetrics. internal/render's
// credhealth PrometheusRule template consumes these too (injected via template
// data), so the exporter and the alert rules that reference these series can
// never drift apart — see internal/render's TestCredhealthMetricNamesNoDrift.
const (
	// MetricCredExpirySeconds is the {kind,identity,path} gauge of seconds
	// until a credential artifact expires (negative if already expired);
	// omitted for artifacts with no expiry.
	MetricCredExpirySeconds = "vikasa_cred_expiry_seconds"
	// MetricCredArtifactsTotal is the {status} gauge counting scanned
	// artifacts by expiry status.
	MetricCredArtifactsTotal = "vikasa_cred_artifacts_total"
	// MetricCredHealthLastScan is the gauge of the last scan's time, as
	// Unix seconds — liveness of the monitor itself.
	MetricCredHealthLastScan = "vikasa_credhealth_last_scan_timestamp_seconds"
)

// WriteMetrics renders the report as Prometheus text-exposition metrics on w,
// using now as the reference clock for time-to-expiry. Output is deterministic:
// records arrive already sorted from Scan, and now is injected. It is intended
// to be written to a node_exporter textfile-collector .prom file.
//
// Emitted: MetricCredExpirySeconds, MetricCredArtifactsTotal, and
// MetricCredHealthLastScan (see their doc comments for shape).
func WriteMetrics(w io.Writer, report *Report, now time.Time) error {
	var b strings.Builder

	fmt.Fprintf(&b, "# HELP %s Seconds until the credential artifact expires (negative if already expired).\n", MetricCredExpirySeconds)
	fmt.Fprintf(&b, "# TYPE %s gauge\n", MetricCredExpirySeconds)
	for _, r := range report.Records {
		if r.Expiry == nil {
			continue
		}
		secs := int64(r.Expiry.Sub(now).Seconds())
		fmt.Fprintf(&b, `%s{kind="%s",identity="%s",path="%s"} %d`+"\n",
			MetricCredExpirySeconds, escapeLabel(r.Kind), escapeLabel(r.Identity), escapeLabel(r.Path), secs)
	}

	fmt.Fprintf(&b, "# HELP %s Count of scanned credential artifacts by expiry status.\n", MetricCredArtifactsTotal)
	fmt.Fprintf(&b, "# TYPE %s gauge\n", MetricCredArtifactsTotal)
	for _, s := range []Status{StatusOK, StatusExpiring, StatusExpired, StatusNoExpiry, StatusError} {
		fmt.Fprintf(&b, `%s{status="%s"} %d`+"\n", MetricCredArtifactsTotal, s, report.Counts[s])
	}

	fmt.Fprintf(&b, "# HELP %s Unix time of the last credhealth scan.\n", MetricCredHealthLastScan)
	fmt.Fprintf(&b, "# TYPE %s gauge\n", MetricCredHealthLastScan)
	fmt.Fprintf(&b, "%s %d\n", MetricCredHealthLastScan, now.Unix())

	_, err := io.WriteString(w, b.String())
	return err
}

// escapeLabel escapes a Prometheus label value per the text exposition format:
// backslash, double-quote and newline only.
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
