package nzbget

import (
	"context"
	"fmt"
	"strings"
)

// LogEntry is one line of a job's log as NZBGet's loadlog returns it.
type LogEntry struct {
	ID   int    `json:"ID"`
	Kind string `json:"Kind"`
	Time int64  `json:"Time"`
	Text string `json:"Text"`
}

type loadLogResponse struct {
	Result []LogEntry `json:"result"`
}

// LoadLog returns the last n lines NZBGet logged for one job. It reads the
// per job log NZBGet keeps on disk (NzbLog=yes, the default), so it still
// answers after the job has moved to history. An empty answer means the log
// is off or was already cleaned up, and says nothing either way.
func (c *Client) LoadLog(ctx context.Context, nzbID, n int) ([]LogEntry, error) {
	var resp loadLogResponse
	if err := c.call(ctx, "loadlog", []any{nzbID, 0, n}, &resp); err != nil {
		return nil, fmt.Errorf("load log: %w", err)
	}
	return resp.Result, nil
}

// Tool is one external program NZBGet reports in sysinfo.
type Tool struct {
	Name    string `json:"Name"`
	Version string `json:"Version"`
	Path    string `json:"Path"`
}

// SysInfo is the part of NZBGet's sysinfo answer Bindery reads. sysinfo
// exists from NZBGet 24; older versions answer the call with an RPC error.
type SysInfo struct {
	Tools []Tool `json:"Tools"`
}

type sysInfoResponse struct {
	Result SysInfo `json:"result"`
}

// SysInfo calls NZBGet's sysinfo. Callers treat an error as "cannot tell",
// because NZBGet before 24 has no such call.
func (c *Client) SysInfo(ctx context.Context) (*SysInfo, error) {
	var resp sysInfoResponse
	if err := c.call(ctx, "sysinfo", nil, &resp); err != nil {
		return nil, fmt.Errorf("sysinfo: %w", err)
	}
	return &resp.Result, nil
}

// MissingUnpackers lists the unpackers sysinfo reports without a path, in
// NZBGet's own names ("UnRAR", "7-Zip"). NZBGet fills Path only when it found
// the configured UnrarCmd or SevenZipCmd, so an empty Path is a missing or
// misconfigured program (SystemInfo::GetToolPath).
func (s *SysInfo) MissingUnpackers() []string {
	if s == nil {
		return nil
	}
	var missing []string
	for _, t := range s.Tools {
		if (t.Name == "UnRAR" || t.Name == "7-Zip") && strings.TrimSpace(t.Path) == "" {
			missing = append(missing, t.Name)
		}
	}
	return missing
}

// FailureEvidence is what a failed job's log says about why it failed.
type FailureEvidence int

const (
	// EvidenceUnknown means the log is empty or names no cause Bindery
	// recognises.
	EvidenceUnknown FailureEvidence = iota
	// EvidenceContent means the log shows the release itself was at fault:
	// a CRC error, a missing volume, too little recovery data.
	EvidenceContent
	// EvidenceHost means the log shows NZBGet's own machine was at fault:
	// an unpacker that would not start or ran out of memory, a file it could
	// not move, a par2 file or memory error.
	EvidenceHost
)

// Markers, copied from NZBGet's source (daemon/postprocess/UnpackController.cpp,
// ParChecker.cpp, PrePostProcessor.cpp, daemon/util/ScriptController.cpp and
// daemon/queue/Scanner.cpp). Unpacker output reaches the job log behind a
// "Unrar: " or "7-Zip: " prefix, so these are matched as substrings.
var (
	hostFaultMarkers = []string{
		// unrar exit 8 is out of memory, 9 is "create file error".
		"Unrar error code: 8",
		"Unrar error code: 9",
		// Moving the unpacked files out of _unpack, or creating it.
		"Could not move file ",
		"Could not create directory ",
		// Renaming the NZB in NzbDir (Scanner::AddFileToQueue).
		"Could not rename file ",
		// libpar2 eFileIOError and eMemoryError (Par2CmdLineErrStr).
		": an error occurred when accessing files",
		": out of memory",
	}
	contentMarkers = []string{
		// unrar: the conditions UnpackController cancels on, and volumes.
		" - CRC failed",
		" - checksum failed",
		// unrar 5 and later word a file CRC error this way (MCRCFailed in
		// unrar's loclang.hpp). NZBGet does not cancel on it, so unrar
		// finishes and also exits with code 3, matched below.
		" - checksum error",
		" : packed data CRC failed in volume",
		" : packed data checksum error in volume",
		"You need to start extraction from a previous volume",
		"Cannot find volume",
		"is not RAR archive",
		"Unexpected end of archive",
		"Unrar error code: 3", // unrar's CRC error exit code
		// 7-Zip's archive errors.
		"7-Zip: ERROR: CRC Failed",
		"7-Zip: ERROR: Data Error",
		"Data Error : ",
		"CRC Failed : ",
		"Headers Error",
		"Can not open the file as archive",
		"Unexpected end of data",
		"Missing volume",
		// Obfuscated archives NZBGet could not put back together.
		"due to renamed archive files",
		// par2 verdicts about the data (Par2CmdLineErrStr 2, 4 and 5).
		": data files are damaged and there is insufficient recovery data",
		": the PAR2 files did not contain sufficient information",
		": repair completed but the data files still appear to be damaged",
		// Too many missing articles to bother repairing.
		"Skipping par-check for ",
	}
	// sevenZipExitMarker alone, with no 7-Zip content marker, is host
	// evidence: 7-Zip reports a CRC or data error on its own line before
	// exiting, so an exit code without one points at the program or the disk.
	sevenZipExitMarker = "7-Zip error code: "
)

// unpackerCouldNotStart reports whether a log line says unrar or 7-Zip could
// not be run at all. NZBGet writes "Could not start <program>" for every
// program it runs, post processing scripts included, and scripts run on
// failed jobs too, so only the unpacker's own lines count: the exec failure
// arrives through the unpacker's log prefix ("Unrar: Could not start
// /usr/bin/unrar: No such file or directory", from the child in
// ScriptController, which also covers the Windows CreateProcess message),
// and an unparsable command line is logged before the prefix is set
// ("Could not start unrar, failed to parse command line",
// UnpackController::PrepareCmdParams).
func unpackerCouldNotStart(text string) bool {
	for _, p := range []string{"Unrar: Could not start ", "7-Zip: Could not start ", "Could not start unrar", "Could not start 7-Zip"} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// ClassifyFailureLog reads a failed job's log for the cause. A host marker
// wins over a content marker, because a broken host can make a good archive
// look damaged but not the other way round, and the cost of a wrong
// blocklist row (permanent, global) is higher than one more retry. detail is
// the line that decided it, for logs and the client's health message.
func ClassifyFailureLog(entries []LogEntry) (evidence FailureEvidence, detail string) {
	var contentLine, sevenZipExit string
	for _, e := range entries {
		text := e.Text
		if unpackerCouldNotStart(text) {
			return EvidenceHost, text
		}
		for _, m := range hostFaultMarkers {
			if strings.Contains(text, m) {
				return EvidenceHost, text
			}
		}
		if contentLine == "" {
			for _, m := range contentMarkers {
				if strings.Contains(text, m) {
					contentLine = text
					break
				}
			}
		}
		if sevenZipExit == "" && strings.Contains(text, sevenZipExitMarker) {
			sevenZipExit = text
		}
	}
	if contentLine != "" {
		return EvidenceContent, contentLine
	}
	if sevenZipExit != "" {
		return EvidenceHost, sevenZipExit
	}
	return EvidenceUnknown, ""
}
