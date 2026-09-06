package migrate

import (
	"github.com/vavallee/bindery/internal/httpsec"
)

// validateMigratedURL is the SSRF guard a migration runs over every outbound
// URL it is about to store. It is the same check, under the same policy, that
// the API create and update handlers apply to the identical fields
// (internal/api/indexers.go, internal/api/download_clients.go): a LAN or
// loopback indexer is an ordinary self-hosted setup, while link-local and
// cloud-metadata destinations never are.
//
// Until #2349 the migration was the one door into the indexer and download
// client tables that skipped this, and its input is an uploaded database file
// rather than a form an operator filled in field by field.
//
// It is a package var only so tests can substitute a check that does not need
// DNS. ValidateOutboundURL resolves the hostname and fails closed when the
// lookup fails, so fixture hosts like "sab.local" would otherwise make the
// migration tests depend on a working resolver. Never reassign it from
// production code.
var validateMigratedURL = func(raw string) error {
	return httpsec.ValidateOutboundURL(raw, httpsec.PolicyLANLoopback)
}
