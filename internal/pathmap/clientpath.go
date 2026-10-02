package pathmap

import (
	"path"
	"strings"
)

// JoinClientPath joins rel onto base in the download client's own namespace,
// not Bindery's: a Windows base written with backslashes keeps backslashes,
// anything else uses forward slashes. Used to build folders a client derives
// from its settings, such as a default save path plus a category name.
func JoinClientPath(base, rel string) string {
	base = strings.TrimSpace(base)
	rel = strings.Trim(strings.TrimSpace(rel), `/\`)
	if rel == "" {
		return base
	}
	sep := "/"
	if IsWindowsPath(base) && !strings.Contains(base, "/") {
		sep = `\`
	}
	return strings.TrimRight(base, `/\`) + sep + rel
}

// IsAbsClientPath reports whether p is absolute in either a POSIX or a Windows
// download client's namespace.
func IsAbsClientPath(p string) bool {
	p = strings.TrimSpace(p)
	return strings.HasPrefix(p, "/") || IsWindowsPath(p)
}

// CleanClientPath is path.Clean in the download client's namespace rather than
// Bindery's. A POSIX client path is cleaned with forward slashes whatever OS
// Bindery runs on: filepath.Clean on a Windows Bindery would turn the
// client's "/downloads/x" into the drive relative "\downloads\x", which no
// POSIX remap rule matches, so the file is never found (#2902). A Windows
// client path (a drive letter or a network share) is cleaned with Windows
// rules and keeps the separator it was written with.
//
// Only a path as the client reports it goes through here. Once it has been
// remapped it is a path on Bindery's own host, and filepath applies.
func CleanClientPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !isWindowsSide(p) {
		return path.Clean(p)
	}
	return cleanWindowsClientPath(p, separatorStyle(p))
}

// ClientPathJoin joins elem onto base in the download client's namespace and
// cleans the result: the client side counterpart of filepath.Join. base
// decides the namespace. A Windows base (drive letter or share) is joined
// with Windows rules in the separator style it was written with, anything
// else with path.Join. Joining in Bindery's namespace instead is what broke
// torrent imports on a Windows Bindery whose client runs in Docker (#2902).
func ClientPathJoin(base string, elem ...string) string {
	base = strings.TrimSpace(base)
	parts := append([]string{base}, elem...)
	if !isWindowsSide(base) {
		return path.Join(parts...)
	}
	return cleanWindowsClientPath(strings.Join(parts, "/"), separatorStyle(base))
}

// ClientPathDir is path.Dir in the download client's namespace; see
// CleanClientPath. The parent of a root is the root itself.
func ClientPathDir(p string) string {
	c := CleanClientPath(p)
	if c == "" {
		return "."
	}
	if !isWindowsSide(c) {
		return path.Dir(c)
	}
	return cleanWindowsClientPath(c+"/..", separatorStyle(c))
}

// ClientPathBase is path.Base in the download client's namespace; see
// CleanClientPath. It returns "" for a root ("/", `C:\`), which has no last
// element to name.
func ClientPathBase(p string) string {
	c := CleanClientPath(p)
	if c == "" || ClientPathDir(c) == c {
		return ""
	}
	return path.Base(strings.ReplaceAll(c, `\`, "/"))
}

// cleanWindowsClientPath cleans a Windows path with `\` and `/` treated as
// the same separator. It keeps the drive designator, or the leading pair of
// separators that marks a share, lets ".." climb no higher than the drive
// root or that leading pair, and renders the result with sep.
func cleanWindowsClientPath(p, sep string) string {
	s := strings.ReplaceAll(p, `\`, "/")
	var vol string
	switch {
	case IsWindowsPath(s):
		vol, s = s[:2], s[2:]
	case strings.HasPrefix(s, "//"):
		// path.Clean would collapse the share's leading "//" to "/".
		vol, s = "/", s[1:]
	}
	s = vol + path.Clean("/"+strings.TrimLeft(s, "/"))
	if sep == `\` {
		s = strings.ReplaceAll(s, "/", `\`)
	}
	return s
}
