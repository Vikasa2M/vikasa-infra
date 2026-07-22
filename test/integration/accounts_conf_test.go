//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats-server/v2/server"
)

// TestGeneratedAccountsConfParses feeds the generator's golden accounts.conf
// (the dev/non-operator form) through the real NATS config parser, proving the
// rendered account/permission syntax — including the subscribe-only DMZ users'
// `publish { deny: [">"] }` blocks — is valid server configuration. The golden
// is a template (user identities are minted by B2/issuance), so placeholder
// identities are injected the way B2 would; the permissions blocks under test
// pass through byte-for-byte.
func TestGeneratedAccountsConfParses(t *testing.T) {
	for _, golden := range []string{
		"../../cmd/gen/testdata/golden-dmz/accounts.conf",
		"../../cmd/gen/testdata/golden-dmz-baremetal/accounts.conf",
		"../../cmd/gen/testdata/golden/accounts.conf",
	} {
		body, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("read %s: %v", golden, err)
		}
		// The rendered file is the accounts/authz fragment; give it the minimal
		// server scaffolding a real deployment supplies around it, and fill in
		// the per-user identity B2 mints ("{" at user-entry indentation).
		var b strings.Builder
		b.WriteString("listen: 127.0.0.1:-1\n")
		n := 0
		for _, line := range strings.Split(string(body), "\n") {
			b.WriteString(line + "\n")
			if line == "      {" {
				n++
				fmt.Fprintf(&b, "        user: u%d, password: p%d\n", n, n)
			}
		}
		conf := b.String()
		path := filepath.Join(t.TempDir(), "accounts.conf")
		if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
			t.Fatalf("write conf: %v", err)
		}
		if _, err := server.ProcessConfigFile(path); err != nil {
			t.Errorf("%s does not parse as NATS config: %v", golden, err)
		}
	}
}
