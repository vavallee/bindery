package slskd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
)

// GrabScheme starts the opaque download address of a Soulseek result. The
// address is Bindery's own: it names a peer and the files of one folder, and it
// is only ever built from a search result Bindery recorded itself.
const GrabScheme = "slskd://"

// RedactedGrabURL is what a response shows in place of the address. The real
// one lists peer file paths, which can be long and are nobody's business in a
// browser.
const RedactedGrabURL = GrabScheme + "result"

// Grab is what a Soulseek result asks slskd for.
type Grab struct {
	User  string      `json:"u"`
	Files []BatchFile `json:"f"`
}

// maxFilenameBytes bounds one peer path. Soulseek paths are short; a long one
// is a hostile listing.
const maxFilenameBytes = 1024

// EncodeGrab builds the address of a result. It refuses what Decode would.
func EncodeGrab(g Grab) (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	return GrabScheme + base64.RawURLEncoding.EncodeToString(b), nil
}

// IsGrabURL reports whether s is a Soulseek download address.
func IsGrabURL(s string) bool { return strings.HasPrefix(s, GrabScheme) }

// DecodeGrab reads an address and validates it.
func DecodeGrab(s string) (Grab, error) {
	var g Grab
	if !IsGrabURL(s) {
		return g, errors.New("not a Soulseek download address")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, GrabScheme))
	if err != nil {
		return g, errors.New("the Soulseek download address is damaged")
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return g, errors.New("the Soulseek download address is damaged")
	}
	return g, g.Validate()
}

// Validate bounds what a grab may ask for. Every filename is the peer's and is
// sent to slskd as is, so it is held to what a real shared path looks like: no
// control characters, no traversal segments, no drive or root prefix.
func (g Grab) Validate() error {
	if strings.TrimSpace(g.User) == "" || len(g.User) > 256 || hasControl(g.User) {
		return errors.New("the Soulseek result names no valid user")
	}
	if len(g.Files) == 0 {
		return errors.New("the Soulseek result lists no files")
	}
	if len(g.Files) > MaxGrabFiles {
		return fmt.Errorf("the Soulseek result lists %d files, more than the %d Bindery downloads at once", len(g.Files), MaxGrabFiles)
	}
	var total int64
	seen := map[string]bool{}
	for _, f := range g.Files {
		if err := ValidateFilename(f.Filename); err != nil {
			return err
		}
		if f.Size <= 0 {
			return errors.New("the Soulseek result lists a file with no size")
		}
		if seen[f.Filename] {
			return errors.New("the Soulseek result lists a file twice")
		}
		seen[f.Filename] = true
		total += f.Size
		if total > MaxGrabBytes || total < 0 {
			return fmt.Errorf("the Soulseek result is larger than the %d GiB Bindery downloads at once", MaxGrabBytes>>30)
		}
	}
	return nil
}

// ValidateFilename rejects a peer path that could escape a folder.
func ValidateFilename(name string) error {
	if name == "" || len(name) > maxFilenameBytes || hasControl(name) {
		return errors.New("the Soulseek result has an unusable file name")
	}
	p := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(p, "/") || (len(p) >= 2 && p[1] == ':') {
		return errors.New("the Soulseek result has an absolute file path")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return errors.New("the Soulseek result has a file path that climbs out of its folder")
		}
	}
	return nil
}

// Ext is the lower case extension shared by the files, from the first one.
func (g Grab) Ext() string {
	if len(g.Files) == 0 {
		return ""
	}
	p := strings.ReplaceAll(g.Files[0].Filename, `\`, "/")
	return strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// DownloadFolder is the folder under slskd's downloads folder a download lands
// in. It carries the download's id and nothing else: no title, no user name.
func DownloadFolder(downloadID int64) string {
	return fmt.Sprintf("bindery/%d", downloadID)
}
