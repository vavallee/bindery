package slskd

import (
	"context"
	"net/http"
	"strings"
)

// ListBatches reads every download slskd holds and groups the transfers by
// batch id. Transfers with no batch (made in slskd's own UI) are left out, so
// Bindery only ever sees what it started.
func (c *Client) ListBatches(ctx context.Context) (map[string][]Transfer, error) {
	var users []struct {
		Username    string `json:"username"`
		Directories []struct {
			Files []Transfer `json:"files"`
		} `json:"directories"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/api/v0/transfers/downloads", 32<<20, &users); err != nil {
		return nil, err
	}
	out := map[string][]Transfer{}
	for _, u := range users {
		for _, d := range u.Directories {
			for _, t := range d.Files {
				if t.BatchID == "" {
					continue
				}
				if t.Username == "" {
					t.Username = u.Username
				}
				key := strings.ToLower(t.BatchID)
				out[key] = append(out[key], t)
			}
		}
	}
	return out, nil
}

// Progress is a batch's transfers rolled up into one download state.
type Progress struct {
	Group     Group
	Files     int
	Succeeded int
	Failed    int
	// Total and Done are bytes, summed over the files.
	Total, Done int64
	// Speed is the summed average speed of the files in progress, bytes per
	// second.
	Speed float64
	// QueuePosition is the best (lowest) place in the peer's upload queue among
	// queued files, nil when slskd reported none.
	QueuePosition *int
	// FailureWord says why the first failed file failed.
	FailureWord string
}

// Summarize rolls transfers up. Any failed file fails the download, because a
// half download is not imported; all files must succeed for it to succeed.
func Summarize(transfers []Transfer) Progress {
	p := Progress{Files: len(transfers)}
	var starting, active int
	for _, t := range transfers {
		p.Total += t.Size
		switch t.State.Group() {
		case GroupSucceeded:
			p.Succeeded++
			p.Done += t.Size
		case GroupFailed:
			p.Failed++
			p.Done += min(max(t.BytesTransferred, 0), t.Size)
			if p.FailureWord == "" {
				p.FailureWord = t.State.FailureWord()
			}
		case GroupActive:
			active++
			p.Done += min(max(t.BytesTransferred, 0), t.Size)
			p.Speed += t.AverageSpeed
		case GroupStarting:
			starting++
		default:
			if t.PlaceInQueue != nil && (p.QueuePosition == nil || *t.PlaceInQueue < *p.QueuePosition) {
				pos := *t.PlaceInQueue
				p.QueuePosition = &pos
			}
		}
	}
	switch {
	case p.Files == 0:
		p.Group = GroupFailed
		p.FailureWord = "is gone from slskd"
	case p.Failed > 0:
		p.Group = GroupFailed
	case p.Succeeded == p.Files:
		p.Group = GroupSucceeded
	case active > 0:
		p.Group = GroupActive
	case starting > 0:
		p.Group = GroupStarting
	default:
		p.Group = GroupWaiting
	}
	return p
}
