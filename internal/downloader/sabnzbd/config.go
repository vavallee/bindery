package sabnzbd

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/pathmap"
)

// completeDirConfig is the only part of SABnzbd's get_config reply Bindery
// reads. An unfiltered get_config returns news server and email passwords, so
// the request names one section and keyword, and the decode target has no
// field that could hold anything else.
type completeDirConfig struct {
	Status *bool  `json:"status"`
	Error  string `json:"error"`
	Config struct {
		Misc struct {
			CompleteDir string `json:"complete_dir"`
		} `json:"misc"`
	} `json:"config"`
}

// categoryDirConfig is the one category field Bindery reads.
type categoryDirConfig struct {
	Status *bool `json:"status"`
	Config struct {
		Categories []struct {
			Name string `json:"name"`
			Dir  string `json:"dir"`
		} `json:"categories"`
	} `json:"config"`
}

// CompleteDir returns the folder SABnzbd moves finished jobs in category into:
// the category's own folder when it sets an absolute one, that folder under
// complete_dir when it is relative, otherwise complete_dir itself. The bool
// reports whether the category's own folder took part.
//
// It needs the full API key. SABnzbd refuses get_config for an NZB only key,
// and that refusal comes back as an error so the caller can say "unknown"
// rather than report a false failure. An empty result with no error means
// SABnzbd reported a relative complete_dir, which Bindery cannot resolve
// without knowing SABnzbd's own home folder.
func (c *Client) CompleteDir(ctx context.Context, category string) (string, bool, error) {
	var misc completeDirConfig
	params := url.Values{"mode": {"get_config"}, "section": {"misc"}, "keyword": {"complete_dir"}}
	if err := c.apiCall(ctx, params, &misc); err != nil {
		return "", false, fmt.Errorf("read complete_dir: %w", httpsec.RedactURLError(err))
	}
	if misc.Status != nil && !*misc.Status {
		return "", false, fmt.Errorf("SABnzbd refused get_config: %s", strings.TrimSpace(misc.Error))
	}

	catDir := ""
	// The category is used exactly as AddURL sends it, spaces included.
	if category != "" {
		var cats categoryDirConfig
		params := url.Values{"mode": {"get_config"}, "section": {"categories"}, "keyword": {category}}
		// A category SABnzbd does not know answers with status false. The
		// category check reports that on its own, so here it only means the
		// category has no folder of its own.
		if err := c.apiCall(ctx, params, &cats); err == nil {
			for _, cat := range cats.Config.Categories {
				if strings.EqualFold(cat.Name, category) {
					catDir = cat.Dir
					break
				}
			}
		}
	}
	dir := resolveCompleteDir(strings.TrimSpace(misc.Config.Misc.CompleteDir), catDir)
	return dir, dir != "" && strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(catDir), "*")) != "", nil
}

// resolveCompleteDir applies SABnzbd's folder rules. A trailing asterisk on a
// category folder only tells SABnzbd not to create a job subfolder, so it is
// dropped. An absolute category folder wins; a relative one sits under
// complete_dir, joined with the separator complete_dir uses so a Windows
// SABnzbd keeps a Windows path.
func resolveCompleteDir(completeDir, catDir string) string {
	catDir = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(catDir), "*"))
	if pathmap.IsAbsClientPath(catDir) {
		return catDir
	}
	if !pathmap.IsAbsClientPath(completeDir) {
		return ""
	}
	return pathmap.JoinClientPath(completeDir, catDir)
}
