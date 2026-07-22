// Package render contains substrate-specific renderers that materialize a
// substrate-independent plan.Plan into named file contents.
//
// Per-cluster substrate renderers implement SubstrateRenderer (see substrate.go)
// and are invoked by Dispatch. Whole-plan renderers (DNSRenderer, RunbookRenderer)
// expose their own Render methods and are called directly by the CLI.
package render
