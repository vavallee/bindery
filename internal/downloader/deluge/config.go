package deluge

import (
	"context"
	"fmt"
	"strings"
)

// Location is where deluged leaves a finished torrent, as DownloadLocation
// works it out.
type Location struct {
	Path string
	// Source names where Path came from, as an English phrase.
	Source string
	// Note and NoteFix are set when a grab will not behave the way the
	// configuration suggests, or when part of the answer could not be checked.
	Note    string
	NoteFix string
}

// LabelID is the form of a category Deluge's Label plugin knows it by. The
// plugin lowercases a label when it is created but its set_torrent and
// get_options take the id exactly as given, so a category typed as "Books"
// only reaches the "books" label when Bindery lowercases it first (#2665).
func LabelID(label string) string {
	return strings.ToLower(label)
}

// Labels lists the labels Deluge's Label plugin holds, as label.get_labels
// returns them (always lowercase). An error usually means the plugin is off,
// in which case the method does not exist. The plugin answers a list, empty
// or not, so a null answer is an error too rather than "no labels".
func (c *Client) Labels(ctx context.Context) ([]string, error) {
	var labels *[]string
	if err := c.call(ctx, true, "label.get_labels", []any{}, &labels); err != nil {
		return nil, fmt.Errorf("list deluge labels: %w", err)
	}
	if labels == nil {
		return nil, fmt.Errorf("list deluge labels: no label list in the reply")
	}
	return *labels, nil
}

// DownloadLocation returns where deluged leaves a torrent that Bindery adds
// with label, looked up as LabelID, the form AddTorrent sends to
// label.set_torrent.
//
// Order of precedence matches Deluge: a label whose options apply a move
// completed path wins, then the global move_completed_path when "move
// completed" is on, then download_location. Only the needed keys are
// requested.
func (c *Client) DownloadLocation(ctx context.Context, label string) (Location, error) {
	var cfg struct {
		DownloadLocation  string `json:"download_location"`
		MoveCompleted     bool   `json:"move_completed"`
		MoveCompletedPath string `json:"move_completed_path"`
	}
	keys := []string{"download_location", "move_completed", "move_completed_path"}
	if err := c.call(ctx, true, "core.get_config_values", []any{keys}, &cfg); err != nil {
		return Location{}, fmt.Errorf("read deluge download location: %w", err)
	}
	loc := Location{Path: strings.TrimSpace(cfg.DownloadLocation), Source: "the client default download location"}
	if cfg.MoveCompleted && strings.TrimSpace(cfg.MoveCompletedPath) != "" {
		loc.Path, loc.Source = strings.TrimSpace(cfg.MoveCompletedPath), "the client default move completed path"
	}

	if label == "" {
		return loc, nil
	}
	label = LabelID(label)
	var opts struct {
		ApplyMoveCompleted bool   `json:"apply_move_completed"`
		MoveCompleted      bool   `json:"move_completed"`
		MoveCompletedPath  string `json:"move_completed_path"`
	}
	if err := c.call(ctx, true, "label.get_options", []any{label}, &opts); err != nil {
		loc.Note = fmt.Sprintf("Bindery could not read the options of the label %q, so a move path set on that label is not checked.", label)
		loc.NoteFix = "Check the settings of that label in Deluge by hand."
		return loc, nil
	}
	if opts.ApplyMoveCompleted && opts.MoveCompleted && strings.TrimSpace(opts.MoveCompletedPath) != "" {
		loc.Path, loc.Source = strings.TrimSpace(opts.MoveCompletedPath), fmt.Sprintf("the move completed path of the label %q", label)
	}
	return loc, nil
}
