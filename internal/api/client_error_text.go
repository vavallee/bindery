package api

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/vavallee/bindery/internal/models"
)

// hostPortPattern matches a network address with a port: an IPv4 address, a
// bracketed IPv6 address, or a host name that starts with a letter (so a time
// such as 10:30, or the time in an RFC 3339 timestamp, is not taken for one).
// Download client URLs always carry a port (clienthost.Authority), so a URL
// without one, such as a documentation link in a client's message, is not
// matched.
var hostPortPattern = regexp.MustCompile(
	`\b\d{1,3}(?:\.\d{1,3}){3}:\d{1,5}\b` +
		`|\[[0-9A-Fa-f:.%]+\]:\d{1,5}` +
		`|\b[A-Za-z][A-Za-z0-9-]*(?:\.[A-Za-z0-9-]+)*:\d{2,5}\b`)

// dnsLookupPattern matches Go's resolver error, "lookup HOST on SERVER" or
// "lookup HOST: no such host".
var dnsLookupPattern = regexp.MustCompile(`\blookup [^\s:]+(?: on |: no such host)`)

// looksLikeClientError reports whether msg reads like a download client's or
// a network library's own error text, which names where the client lives: a
// dial or DNS failure, or a host:port. It is used for failed downloads stored
// before send failures were marked (downloader.SendFailureMessage), and only
// for those, since an import message can hold a path that looks like a
// host:port.
func looksLikeClientError(msg string) bool {
	return strings.Contains(msg, "dial tcp") ||
		dnsLookupPattern.MatchString(msg) ||
		hostPortPattern.MatchString(msg)
}

// historyDataForViewer masks the message of a downloadFailed history event
// for a caller who is not an admin, when it holds the download client's own
// error text (rows written before the send failure message became fixed
// text). Download client configuration is admin only. Other events, and
// messages that are Bindery's own, pass through unchanged.
func historyDataForViewer(ctx context.Context, eventType, data string) string {
	if eventType != models.HistoryEventDownloadFailed || isAdminCtx(ctx) || data == "" {
		return data
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(data), &fields); err != nil {
		if looksLikeClientError(data) {
			return ""
		}
		return data
	}
	msg, ok := fields["message"].(string)
	if !ok || !looksLikeClientError(msg) {
		return data
	}
	fields["message"] = sendFailedMessage
	out, err := json.Marshal(fields)
	if err != nil {
		return ""
	}
	return string(out)
}
