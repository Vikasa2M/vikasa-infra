package render

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"

	"github.com/Vikasa2M/vikasa-infra/internal/accounts"
)

//go:embed accounts.tmpl
var accountsTmpl string

var accountsTemplate = template.Must(template.New("accounts").Parse(accountsTmpl))

// AccountsRenderer renders the NATS account model to an inline accounts{} config
// (accounts.conf). It is a whole-model renderer (not a per-cluster
// SubstrateRenderer), called directly by the CLI like RunbookRenderer.
type AccountsRenderer struct{}

// Render produces the accounts.conf file bytes.
func (AccountsRenderer) Render(m *accounts.Model) (map[string][]byte, error) {
	var buf bytes.Buffer
	if err := accountsTemplate.Execute(&buf, m); err != nil {
		return nil, fmt.Errorf("accounts render: %w", err)
	}
	return map[string][]byte{"accounts.conf": buf.Bytes()}, nil
}
