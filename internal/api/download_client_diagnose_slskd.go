package api

import (
	"context"
	"fmt"

	"github.com/vavallee/bindery/internal/downloader"
	"github.com/vavallee/bindery/internal/downloader/slskd"
)

// Check codes for the slskd rows of the Diagnose checklist (#1717).
const (
	diagCodeSlskdVersion = "slskd_version"
	diagCodeSlskdLogin   = "slskd_login"
	diagCodeSlskdRole    = "slskd_role"
	diagCodeSlskdPort    = "slskd_port"
)

// slskdApp reads GET /api/v0/application once per Diagnose run and keeps the
// answer for the version, login and port rows.
func (st *diagState) slskdApp(ctx context.Context) (*slskd.Application, error) {
	if !st.slskdFetched {
		st.slskdFetched = true
		st.slskdAppState, st.slskdAppErr = downloader.SlskdFor(st.client).Application(ctx)
	}
	return st.slskdAppState, st.slskdAppErr
}

// slskdAppOrSkip returns the application state, or a ready made result when it
// is not available. Reachability is the connect row's job, so a failure here
// is reported as skipped rather than repeated.
func (st *diagState) slskdAppOrSkip(ctx context.Context) (*slskd.Application, []diagCheckResult) {
	if st.client.Type != downloader.ClientTypeSlskd {
		return nil, nil
	}
	app, err := st.slskdApp(ctx)
	if err != nil {
		return nil, one(diagCheckResult{Status: diagSkipped, Message: "Skipped because slskd did not answer."})
	}
	return app, nil
}

func checkDiagSlskdVersion(ctx context.Context, st *diagState) []diagCheckResult {
	app, res := st.slskdAppOrSkip(ctx)
	if app == nil {
		return res
	}
	if err := slskd.CheckVersion(app.Version.Current); err != nil {
		return one(diagCheckResult{
			Status:  diagFail,
			Message: errText(err),
			Fix:     fmt.Sprintf("Update slskd to %s or newer.", slskd.MinVersion),
		})
	}
	return one(diagCheckResult{Status: diagPass, Message: fmt.Sprintf("slskd %s is new enough (%s or newer is needed).", app.Version.Current, slskd.MinVersion)})
}

func checkDiagSlskdLogin(ctx context.Context, st *diagState) []diagCheckResult {
	app, res := st.slskdAppOrSkip(ctx)
	if app == nil {
		return res
	}
	switch {
	case !app.Server.IsConnected:
		return one(diagCheckResult{
			Status:  diagFail,
			Message: "slskd is not connected to the Soulseek server.",
			Fix:     "Open slskd and check that it can reach the Soulseek network. If it runs behind a VPN, check the VPN is up.",
		})
	case !app.Server.IsLoggedIn:
		return one(diagCheckResult{
			Status:  diagFail,
			Message: "slskd is connected to the Soulseek server but is not logged in.",
			Fix:     "Check the Soulseek username and password in slskd's settings.",
		})
	}
	return one(diagCheckResult{Status: diagPass, Message: "slskd is connected to the Soulseek network and logged in."})
}

func checkDiagSlskdRole(ctx context.Context, st *diagState) []diagCheckResult {
	if st.client.Type != downloader.ClientTypeSlskd {
		return nil
	}
	if _, err := st.slskdApp(ctx); err != nil {
		return one(diagCheckResult{Status: diagSkipped, Message: "Skipped because slskd did not answer."})
	}
	role, err := downloader.SlskdFor(st.client).ProbeRole(ctx)
	switch role {
	case slskd.RoleWrite:
		return one(diagCheckResult{Status: diagPass, Message: "The API key has a write role, so Bindery can start searches and downloads."})
	case slskd.RoleReadOnly:
		return one(diagCheckResult{
			Status:  diagFail,
			Message: "The API key is read only. Bindery cannot start searches or downloads with it.",
			Fix:     "In slskd's settings, give this API key the readwrite (or administrator) role, then save it on this client again.",
		})
	}
	msg := "Bindery could not verify that the API key has a write role."
	if err != nil {
		msg += " " + errText(err)
	}
	return one(diagCheckResult{
		Status:  diagUnknown,
		Message: msg,
		Fix:     "Check in slskd's settings that this API key has the readwrite or administrator role.",
	})
}

// checkDiagSlskdPort is informational. slskd only sets vpn.forwardedPort when
// its own VPN integration is in use, so a missing value says nothing about a
// router port forward. It is therefore never a warning or a failure.
func checkDiagSlskdPort(ctx context.Context, st *diagState) []diagCheckResult {
	app, res := st.slskdAppOrSkip(ctx)
	if app == nil {
		return res
	}
	if p := app.Vpn.ForwardedPort; p != nil && *p > 0 {
		return one(diagCheckResult{Status: diagPass, Message: fmt.Sprintf("slskd reports a VPN forwarded port (%d).", *p)})
	}
	return one(diagCheckResult{
		Status:  diagUnknown,
		Message: "Bindery cannot detect whether peers can reach slskd. slskd reports a forwarded port only with its VPN integration, so this is expected with a router port forward. Without any forwarded port, most peers cannot connect and downloads stall or fail.",
		Fix:     "Forward slskd's listen port (50300 by default) to the machine running slskd, or use slskd's VPN port forwarding. If downloads already work, ignore this.",
	})
}
