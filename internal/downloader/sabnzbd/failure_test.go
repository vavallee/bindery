package sabnzbd

import "testing"

// TestIsContentFailure pins the #3024 classifier against fail_message strings
// as SABnzbd's own source writes them, including the arguments it appends.
func TestIsContentFailure(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		// The release is broken: blocklist it.
		{"Aborted, cannot be completed - https://sabnzbd.org/not-complete", true},
		{"Download failed - Not on your server(s) - https://sabnzbd.org/not-complete", true},
		{"Download might fail, only 71.2% of required 100.0% available - https://sabnzbd.org/not-complete", true},
		{"Repair failed, not enough repair blocks (412 short)", true},
		{"Repairing failed, Repair Failed. (the par2 output)", true},
		{"Unpacking failed, CRC error", true},
		{"Unpacking failed, unable to find book.part07.rar", true},
		{"Unpacking failed, archive requires a password", true},
		{"Corrupt RAR file", true},
		{"Unusable RAR file", true},
		{"RAR files failed to verify", true},
		{`Some files failed to verify against "book.sfv"`, true},
		{"Aborted, encryption detected", true},
		{"Aborted, unwanted extension detected", true},
		{"  unpacking FAILED, crc error  ", true},

		// The client or the machine: keep the #2710 cooldown.
		{"Unpacking failed, write error or disk is full? No space left", false},
		{"Unpacking failed, disk full", false},
		{"Unpacking failed, file too large for filesystem (FAT?)", false},
		{"Unpacking failed, see logfile", false},
		{"Repairing failed, Disk full", false},
		{"Repairing failed, [Errno 13] Permission denied", false},
		{"Failed to move files", false},
		{"Post-processing was aborted", false},
		{"Duplicate NZB", false},
		{"Script exit code is 1", false},

		// Unknown or translated messages never blocklist.
		{"", false},
		{"Entpacken fehlgeschlagen, CRC-Fehler", false},
		{"unpack failed: missing par2", false},
	}
	for _, tc := range cases {
		if got := IsContentFailure(tc.msg); got != tc.want {
			t.Errorf("IsContentFailure(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
