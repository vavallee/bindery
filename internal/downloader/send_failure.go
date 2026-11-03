package downloader

import "strings"

// sendFailurePrefix marks the error message stored on a download whose
// release the download client refused or could not be reached for. The text
// after it is the client's own failure, which usually names the client's
// address ("could not reach qBittorrent at http://10.0.0.5:8080 ..."), and
// download client configuration is admin only. The marker is what lets the
// queue tell that text apart from Bindery's own import messages when it
// renders a row for someone who is not an admin.
const sendFailurePrefix = "download client error: "

// SendFailureMessage is the error message to store on a download when
// SendDownload fails with err.
func SendFailureMessage(err error) string {
	return sendFailurePrefix + err.Error()
}

// SendFailureDetail splits a stored error message. For a message written by
// SendFailureMessage it returns the client's own text and true; any other
// message comes back unchanged with false.
func SendFailureDetail(msg string) (string, bool) {
	if detail, ok := strings.CutPrefix(msg, sendFailurePrefix); ok {
		return detail, true
	}
	return msg, false
}
