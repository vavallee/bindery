package downloader

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

// NZBGet's sysinfo (NZBGet 24 and later) reports whether it can find UnRAR
// and 7-Zip. It is not cheap: answering it makes NZBGet run `which`, unrar
// and 7z, and fetch its public address from ip.nzbget.com. So Bindery asks
// only when it needs the answer, never from the periodic health probe: when
// an unpack failure arrives with no evidence in its log, and when the user
// creates, edits or tests the client (#3024).
const (
	// unpackerCheckTTL is how long a sysinfo answer is reused. An unpacker
	// does not come and go on its own; a fix the user makes is picked up
	// at once through the edit or Test path.
	unpackerCheckTTL = 6 * time.Hour
	// unpackerRetryAfterFailure spaces out calls to an NZBGet whose sysinfo
	// failed (older than 24, or briefly unreachable), so a burst of failed
	// jobs does not become a burst of sysinfo calls.
	unpackerRetryAfterFailure = 15 * time.Minute
	// unpackerCheckTimeout bounds one sysinfo call.
	unpackerCheckTimeout = 15 * time.Second
)

type unpackerEntry struct {
	missing   []string
	known     bool      // a sysinfo call has succeeded at least once
	checkedAt time.Time // last successful call
	triedAt   time.Time // last call, successful or not
}

func (s *HealthStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *HealthStore) callSysinfo(ctx context.Context, client *models.DownloadClient) ([]string, error) {
	if s.sysinfo != nil {
		return s.sysinfo(ctx, client)
	}
	info, err := NzbgetFor(client).SysInfo(ctx)
	if err != nil {
		return nil, err
	}
	return info.MissingUnpackers(), nil
}

// NZBGetUnpackers returns the unpackers an NZBGet client cannot find ("UnRAR",
// "7-Zip"), from a cached sysinfo answer unless force is set or the answer is
// older than unpackerCheckTTL. A failed call keeps the last known answer, so
// a timeout never makes a missing UnRAR look found. Other client types, and an
// NZBGet that has never answered, return nil.
//
// A missing UnRAR is published as an error on the client (AdvisoryUnpackers).
// A missing 7-Zip on its own is only logged: NZBGet's default SevenZipCmd is
// often absent, and RAR releases unpack fine without it.
func (s *HealthStore) NZBGetUnpackers(ctx context.Context, client *models.DownloadClient, force bool) []string {
	if s == nil || client == nil || client.ID == 0 || client.Type != "nzbget" {
		return nil
	}
	now := s.clock()
	s.unpackersMu.Lock()
	entry := s.unpackers[client.ID]
	if entry == nil {
		entry = &unpackerEntry{}
		s.unpackers[client.ID] = entry
	}
	fresh := entry.known && now.Sub(entry.checkedAt) < unpackerCheckTTL
	backingOff := !entry.triedAt.IsZero() && now.Sub(entry.triedAt) < unpackerRetryAfterFailure && entry.triedAt.After(entry.checkedAt)
	if !force && (fresh || backingOff) {
		missing := entry.missing
		s.unpackersMu.Unlock()
		return missing
	}
	entry.triedAt = now
	s.unpackersMu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, unpackerCheckTimeout)
	defer cancel()
	missing, err := s.callSysinfo(callCtx, client)

	s.unpackersMu.Lock()
	if err != nil {
		last := entry.missing
		s.unpackersMu.Unlock()
		slog.Debug("nzbget sysinfo unavailable, keeping the last known unpackers", "client", client.Name, "error", err)
		return last
	}
	prev := entry.missing
	entry.missing, entry.known, entry.checkedAt = missing, true, now
	s.unpackersMu.Unlock()

	if slices.Contains(missing, "UnRAR") {
		s.SetAdvisory(client.ID, AdvisoryUnpackers, models.DownloadClientHealth{
			Status: HealthError,
			Message: fmt.Sprintf("NZBGet cannot find %s, so it cannot unpack RAR releases. "+
				"Check UnrarCmd in NZBGet's settings. Until it can, Bindery does not blocklist releases NZBGet fails to unpack",
				strings.Join(missing, " or ")),
		})
	} else {
		s.ClearAdvisory(client.ID, AdvisoryUnpackers)
	}
	if slices.Contains(missing, "7-Zip") && !slices.Contains(prev, "7-Zip") {
		slog.Warn("NZBGet cannot find 7-Zip; it will fail releases packed as 7z. Check SevenZipCmd in NZBGet's settings",
			"client", client.Name)
	}
	return missing
}
