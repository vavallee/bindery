package slskd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Download limits. A grab asks one peer for the files of one folder, and both
// numbers bound what a hostile or confused listing can make Bindery request
// and then import.
const (
	// MaxGrabFiles is the most files one download may hold.
	MaxGrabFiles = 200
	// MaxGrabBytes is the most total bytes one download may hold.
	MaxGrabBytes int64 = 10 << 30

	maxBatchBytes = 8 << 20
)

// TransferState is slskd's transfer state, a flag set from Soulseek.NET. slskd
// serialises it as text ("Completed, Succeeded") and some versions as a number,
// so both are read.
type TransferState uint32

// The Soulseek.NET TransferStates flags Bindery reads.
const (
	StateRequested    TransferState = 1
	StateQueued       TransferState = 2
	StateInitializing TransferState = 4
	StateInProgress   TransferState = 8
	StateCompleted    TransferState = 16
	StateSucceeded    TransferState = 32
	StateCancelled    TransferState = 64
	StateTimedOut     TransferState = 128
	StateErrored      TransferState = 256
	StateRejected     TransferState = 512
	StateAborted      TransferState = 1024
	StateLocally      TransferState = 2048
	StateRemotely     TransferState = 4096
)

var stateNames = map[string]TransferState{
	"none": 0, "requested": StateRequested, "queued": StateQueued, "initializing": StateInitializing,
	"inprogress": StateInProgress, "completed": StateCompleted, "succeeded": StateSucceeded,
	"cancelled": StateCancelled, "canceled": StateCancelled, "timedout": StateTimedOut,
	"errored": StateErrored, "rejected": StateRejected, "aborted": StateAborted,
	"locally": StateLocally, "remotely": StateRemotely,
}

// UnmarshalJSON reads a state given as text flags or as a number.
func (s *TransferState) UnmarshalJSON(b []byte) error {
	var n uint32
	if err := json.Unmarshal(b, &n); err == nil {
		*s = TransferState(n)
		return nil
	}
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return errors.New("transfer state is neither text nor a number")
	}
	var out TransferState
	for _, part := range strings.Split(text, ",") {
		part = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(part, " ", "")))
		if part == "" {
			continue
		}
		flag, ok := stateNames[part]
		if !ok {
			// A flag from a newer slskd. Unknown means "not completed and not
			// succeeded", which the poller reads as still waiting.
			continue
		}
		out |= flag
	}
	*s = out
	return nil
}

// Has reports whether every bit of f is set.
func (s TransferState) Has(f TransferState) bool { return s&f == f }

// Group is a transfer's place in a download's life.
type Group int

// The states Bindery distinguishes.
const (
	// GroupWaiting is requested, queued locally or remotely, or unknown.
	GroupWaiting Group = iota
	// GroupStarting is the connection and negotiation before bytes flow.
	GroupStarting
	// GroupActive is bytes flowing.
	GroupActive
	// GroupSucceeded is completed with success.
	GroupSucceeded
	// GroupFailed is completed without success: errored, cancelled, timed out,
	// rejected or aborted.
	GroupFailed
)

// Group classifies the state. Completed wins over everything, and only
// Completed with Succeeded is a success.
func (s TransferState) Group() Group {
	switch {
	case s.Has(StateCompleted):
		if s.Has(StateSucceeded) {
			return GroupSucceeded
		}
		return GroupFailed
	case s.Has(StateInProgress):
		return GroupActive
	case s.Has(StateInitializing):
		return GroupStarting
	default:
		return GroupWaiting
	}
}

// FailureWord names why a failed transfer failed, from its flags alone. It never
// carries peer text.
func (s TransferState) FailureWord() string {
	switch {
	case s.Has(StateRejected):
		return "rejected by the peer"
	case s.Has(StateTimedOut):
		return "timed out"
	case s.Has(StateCancelled), s.Has(StateAborted):
		return "cancelled"
	case s.Has(StateErrored):
		return "failed"
	}
	return "did not finish"
}

// Transfer is one file of a batch as slskd reports it.
type Transfer struct {
	ID               string        `json:"id"`
	BatchID          string        `json:"batchId"`
	Username         string        `json:"username"`
	Filename         string        `json:"filename"`
	Size             int64         `json:"size"`
	State            TransferState `json:"state"`
	BytesTransferred int64         `json:"bytesTransferred"`
	AverageSpeed     float64       `json:"averageSpeed"`
	PlaceInQueue     *int          `json:"placeInQueue"`
	Removed          bool          `json:"removed"`
}

// Batch is a batch of downloads from one peer.
type Batch struct {
	ID        string     `json:"id"`
	Username  string     `json:"username"`
	Transfers []Transfer `json:"transfers"`
}

// BatchFile names one file to request. Filename is the peer's own path, sent
// back to slskd exactly as the search returned it.
type BatchFile struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type batchRequest struct {
	ID       string       `json:"id"`
	Username string       `json:"username"`
	Files    []BatchFile  `json:"files"`
	Options  batchOptions `json:"options"`
}

type batchOptions struct {
	Destination string `json:"destination"`
}

// EnqueueResult is slskd's answer to a batch request.
type EnqueueResult struct {
	Batch    *Batch `json:"batch"`
	Failures []struct {
		Filename string `json:"filename"`
		Message  string `json:"message"`
	} `json:"failures"`
}

// ErrPeerOffline is returned when slskd cannot reach the peer.
var ErrPeerOffline = errors.New("the Soulseek peer appears to be offline")

// ErrBatchGone is returned when slskd no longer knows a batch.
var ErrBatchGone = errors.New("slskd no longer has this download")

// EnqueueBatch asks slskd to download files from one peer into destination, a
// folder under slskd's downloads folder. batchID must be a GUID and is Bindery's
// own, so a resend of the same request is a 409 and never a duplicate. Needs
// slskd 0.26.0. Peer files are validated by the caller; slskd also refuses
// traversal.
func (c *Client) EnqueueBatch(ctx context.Context, batchID, username string, files []BatchFile, destination string) (*EnqueueResult, error) {
	if len(files) == 0 || len(files) > MaxGrabFiles {
		return nil, fmt.Errorf("a Soulseek download must hold between 1 and %d files", MaxGrabFiles)
	}
	var res EnqueueResult
	status, err := c.doBody(ctx, http.MethodPost, "/api/v0/transfers/downloads/batches", batchRequest{
		ID: batchID, Username: username, Files: files, Options: batchOptions{Destination: destination},
	}, maxBatchBytes, &res)
	switch {
	case status == http.StatusNotFound:
		return nil, ErrPeerOffline
	case status == http.StatusConflict:
		return nil, errors.New("slskd already has a download with this id")
	case err != nil:
		return nil, fmt.Errorf("start slskd download: %w", err)
	}
	if len(res.Failures) > 0 {
		// slskd answers 207 when some files were refused (already queued, or
		// the peer rejected them) and 200 when all were. A download holds all
		// its files or none, so whatever did start is taken back.
		if res.Batch != nil && res.Batch.ID != "" {
			cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			_ = c.RemoveBatch(cleanCtx, res.Batch.ID)
		}
		return nil, fmt.Errorf("slskd could not queue %d of the %d files", len(res.Failures), len(files))
	}
	return &res, nil
}

// GetBatch reads a batch with its transfers. slskd attaches them to the batch;
// when a version does not, the peer's download list is filtered by batch id.
func (c *Client) GetBatch(ctx context.Context, batchID string) (*Batch, error) {
	var b Batch
	status, err := c.do(ctx, http.MethodGet, "/api/v0/transfers/downloads/batches/"+url.PathEscape(batchID), maxBatchBytes, &b)
	if status == http.StatusNotFound {
		return nil, ErrBatchGone
	}
	if err != nil {
		return nil, err
	}
	if b.Transfers == nil && b.Username != "" {
		list, err := c.userTransfers(ctx, b.Username)
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			if strings.EqualFold(t.BatchID, batchID) {
				b.Transfers = append(b.Transfers, t)
			}
		}
	}
	return &b, nil
}

func (c *Client) userTransfers(ctx context.Context, username string) ([]Transfer, error) {
	var resp struct {
		Directories []struct {
			Files []Transfer `json:"files"`
		} `json:"directories"`
	}
	status, err := c.do(ctx, http.MethodGet, "/api/v0/transfers/downloads/"+url.PathEscape(username), maxBatchBytes, &resp)
	if status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Transfer
	for _, d := range resp.Directories {
		out = append(out, d.Files...)
	}
	return out, nil
}

// RemoveTransfer cancels one transfer and removes it from slskd. slskd has no
// batch cancel, so a download is removed one file at a time. A transfer that is
// already gone is not an error.
func (c *Client) RemoveTransfer(ctx context.Context, username, id string) error {
	path := "/api/v0/transfers/downloads/" + url.PathEscape(username) + "/" + url.PathEscape(id) + "?remove=true"
	status, err := c.do(ctx, http.MethodDelete, path, 1<<10, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// RemoveBatch cancels and removes every transfer of a batch. A batch slskd no
// longer has is already removed. The first failure is returned after every
// transfer was tried.
func (c *Client) RemoveBatch(ctx context.Context, batchID string) error {
	b, err := c.GetBatch(ctx, batchID)
	if errors.Is(err, ErrBatchGone) {
		return nil
	}
	if err != nil {
		return err
	}
	var first error
	for _, t := range b.Transfers {
		if t.ID == "" {
			continue
		}
		if err := c.RemoveTransfer(ctx, b.Username, t.ID); err != nil && first == nil {
			first = fmt.Errorf("remove transfer %s: %w", strconv.Quote(t.ID), err)
		}
	}
	return first
}
