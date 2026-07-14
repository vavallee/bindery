//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

// writabilityIdentity describes who the process is versus who owns the
// directory, so a "not writable" warning is self-diagnosing (#1427). The
// recurring support case: an operator prepares folders for their stack's
// user (typically 1000:1000, linuxserver-style) but never sets `user:` on
// the container, so Bindery runs as the distroless default UID 65532 and the
// folder genuinely isn't writable — while "the permissions are right" from
// the operator's point of view. Naming both identities in the message turns
// that thread-length back-and-forth into a one-line fix.
func writabilityIdentity(info os.FileInfo) string {
	msg := fmt.Sprintf("process runs as uid=%d gid=%d", os.Getuid(), os.Getgid())
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		msg += fmt.Sprintf("; directory is owned by uid=%d gid=%d mode=%s", st.Uid, st.Gid, info.Mode().Perm())
		if int(st.Uid) != os.Getuid() {
			msg += fmt.Sprintf(" — if that owner is intentional, run the container with user: \"%d:%d\"", st.Uid, st.Gid)
		}
	}
	return msg
}
