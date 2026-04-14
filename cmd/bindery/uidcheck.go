package main

import (
	"log/slog"
	"os"
	"runtime"
	"strconv"
)

// checkPUIDPGID verifies, at startup, that the running UID/GID matches what
// the operator asked for via `BINDERY_PUID`/`BINDERY_PGID`. Distroless images
// can't switch user at runtime (no shell, no gosu), so the container must be
// launched with `--user ${PUID}:${PGID}` or the k8s equivalent. This check
// catches the far-more-common failure mode — env vars set, container not
// launched with `--user`, Bindery silently runs as the default UID 65532 and
// later hits permission-denied on /config or the library mount — and turns
// it into a loud, actionable startup error instead of an opaque runtime one.
//
// When neither variable is set, the check is a no-op: we're not imposing a
// UID policy on users who haven't opted in.
func checkPUIDPGID() {
	if runtime.GOOS != "linux" {
		// Getuid/Getgid return -1 on Windows; PUID/PGID is a Linux-container
		// concept and doesn't translate, so skip the check rather than mislead.
		return
	}

	wantUID, haveUID := os.LookupEnv("BINDERY_PUID")
	wantGID, haveGID := os.LookupEnv("BINDERY_PGID")
	if !haveUID && !haveGID {
		return
	}

	actualUID, actualGID := os.Getuid(), os.Getgid()

	if haveUID {
		uid, err := strconv.Atoi(wantUID)
		if err != nil {
			slog.Error("BINDERY_PUID is not a number", "value", wantUID)
			os.Exit(1)
		}
		if uid != actualUID {
			slog.Error("BINDERY_PUID does not match the running UID — restart the container with the matching --user flag",
				"want_uid", uid,
				"actual_uid", actualUID,
				"docker_hint", "docker run --user "+wantUID+":"+orDefault(wantGID, strconv.Itoa(actualGID))+" ...",
				"compose_hint", "user: \""+wantUID+":"+orDefault(wantGID, strconv.Itoa(actualGID))+"\"",
				"k8s_hint", "spec.template.spec.securityContext.runAsUser: "+wantUID,
			)
			os.Exit(1)
		}
	}

	if haveGID {
		gid, err := strconv.Atoi(wantGID)
		if err != nil {
			slog.Error("BINDERY_PGID is not a number", "value", wantGID)
			os.Exit(1)
		}
		if gid != actualGID {
			slog.Error("BINDERY_PGID does not match the running GID — restart the container with the matching --user flag",
				"want_gid", gid,
				"actual_gid", actualGID,
				"docker_hint", "docker run --user "+orDefault(wantUID, strconv.Itoa(actualUID))+":"+wantGID+" ...",
				"compose_hint", "user: \""+orDefault(wantUID, strconv.Itoa(actualUID))+":"+wantGID+"\"",
				"k8s_hint", "spec.template.spec.securityContext.runAsGroup: "+wantGID,
			)
			os.Exit(1)
		}
	}

	slog.Info("PUID/PGID match confirmed", "uid", actualUID, "gid", actualGID)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
