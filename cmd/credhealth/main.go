// Command credhealth scans a minted credential bundle (see cmd/issue) and
// reports each time-bound artifact's expiry status, exiting non-zero when
// anything is expired, expiring, or unreadable. It is read-only and reads only
// public material (JWT claims + cert metadata) — never the secret seed/key files.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/Vikasa2M/vikasa-infra/internal/credhealth"
)

func main() {
	dir := flag.String("dir", "creds", "credential bundle directory to scan")
	warn := flag.Duration("warn", 720*time.Hour, "near-expiry warning window")
	metricsOut := flag.String("metrics-out", "", "if set, write Prometheus textfile metrics to this path (node_exporter textfile collector)")
	flag.Parse()
	os.Exit(run(*dir, *warn, *metricsOut, time.Now(), os.Stdout, os.Stderr))
}

// run scans dir, writes the table + summary to w and errors to errW, optionally
// writes Prometheus metrics to metricsOut, and returns the process exit code
// (0 healthy, 1 otherwise — including when a requested metrics write fails).
func run(dir string, warn time.Duration, metricsOut string, now time.Time, w, errW io.Writer) int {
	report, err := credhealth.Scan(dir, now, warn)
	if err != nil {
		fmt.Fprintln(errW, "credhealth:", err)
		return 1
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tIDENTITY\tISSUED\tEXPIRES\tSTATUS")
	for _, r := range report.Records {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.Kind, r.Identity, fmtTime(r.Issued), fmtExpiry(r.Expiry), r.Status)
	}
	tw.Flush()

	fmt.Fprintf(w, "\ncredhealth: %d scanned — %d ok, %d expiring, %d expired, %d no-expiry, %d error  (dir=%s warn=%s)\n",
		len(report.Records),
		report.Counts[credhealth.StatusOK],
		report.Counts[credhealth.StatusExpiring],
		report.Counts[credhealth.StatusExpired],
		report.Counts[credhealth.StatusNoExpiry],
		report.Counts[credhealth.StatusError],
		dir, warn)

	if metricsOut != "" {
		if err := writeMetricsFile(metricsOut, report, now); err != nil {
			fmt.Fprintln(errW, "credhealth: metrics:", err)
			return 1
		}
	}
	return report.ExitCode()
}

// writeMetricsFile writes the metrics atomically: render to a uniquely-named
// temp file in the destination directory, then rename into place so the textfile
// collector never reads a partially-written file. CreateTemp's O_EXCL + random
// name closes the symlink-follow / predictable-temp window; 0644 keeps the
// metrics (non-secret public material) readable by the collector process.
func writeMetricsFile(path string, report *credhealth.Report, now time.Time) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".credhealth-*.prom")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o644); err != nil {
		f.Close()
		return err
	}
	if err = credhealth.WriteMetrics(f, report, now); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02")
}

func fmtExpiry(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("2006-01-02")
}
