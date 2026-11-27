package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed pprof_overlay.go.txt
var pprofOverlay []byte

const placeholderIndex = `<!doctype html><html><head><title>Bindery performance build</title></head><body>Built by tools/perf without the web UI.</body></html>`

const apiKey = "perf-harness-key-000000000000000" //nolint:gosec // G101: a fixed key for a throwaway instance on loopback

// repoRoot is the directory of the go.mod that owns the working directory.
func repoRoot(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", errors.New("not inside the bindery module; run from the repository or pass -binary")
	}
	return filepath.Dir(mod), nil
}

// BuildBinary compiles cmd/bindery into out with the profiling listener added
// through a build overlay. Nothing under cmd/ or internal/ is written.
func BuildBinary(ctx context.Context, out string) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}
	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	src := filepath.Join(dir, "pprof_overlay.go")
	if err := os.WriteFile(src, pprofOverlay, 0o644); err != nil {
		return err
	}
	replace := map[string]string{filepath.Join(root, "cmd", "bindery", "zz_perf_pprof.go"): src}
	// The binary refuses to start without an embedded index.html. A checkout
	// that has not run the web build gets a placeholder page, again through
	// the overlay, so the harness needs no npm and leaves the tree untouched.
	index := filepath.Join(root, "internal", "webui", "dist", "index.html")
	if _, err := os.Stat(index); err != nil {
		placeholder := filepath.Join(dir, "index.html")
		if err := os.WriteFile(placeholder, []byte(placeholderIndex), 0o644); err != nil {
			return err
		}
		replace[index] = placeholder
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return err
	}
	overlayPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o644); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-overlay", overlayPath, "-o", out, "./cmd/bindery") //nolint:gosec // G204: fixed arguments, paths under the data directory
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if msg, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, msg)
	}
	return nil
}

// InstanceOpts describes one boot of the binary.
type InstanceOpts struct {
	Binary string
	// WorkDir receives the database copy, the data directory and the log. It
	// is created, and removed again by Stop.
	WorkDir string
	// SeedDB is copied to WorkDir before the boot. Empty boots a new install.
	SeedDB      string
	LibraryDir  string
	DownloadDir string
	// ProxyURL is the outbound proxy, normally the OutboundTrap.
	ProxyURL string
	// Pprof starts the overlay's profiling listener on a free loopback port.
	Pprof bool
	// Prepare runs on the copied database before the boot.
	Prepare func(dbPath string) error
	// LogLevel is the binary's log level; info, the default of a real
	// install, when empty.
	LogLevel string
	// LogCopy, when set, receives the end of the binary's log at Stop.
	LogCopy string
}

// Instance is a running bindery process.
type Instance struct {
	Base      string
	PprofAddr string
	DBPath    string
	// Boot is the time from process start to the first 200 from the health
	// endpoint, which the server only answers once migrations and startup
	// checks are done.
	Boot    time.Duration
	workDir string
	logCopy string
	cmd     *exec.Cmd
	log     *os.File
	done    chan struct{}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // closed two lines down
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	return port, l.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// StartInstance boots the binary and waits for it to answer its health check.
func StartInstance(ctx context.Context, o InstanceOpts) (*Instance, error) {
	if err := os.RemoveAll(o.WorkDir); err != nil {
		return nil, err
	}
	for _, d := range []string{o.WorkDir, o.LibraryDir, o.DownloadDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	inst := &Instance{DBPath: filepath.Join(o.WorkDir, "bindery.db"), workDir: o.WorkDir, logCopy: o.LogCopy, done: make(chan struct{})}
	if o.SeedDB != "" {
		if err := copyFile(o.SeedDB, inst.DBPath); err != nil {
			return nil, fmt.Errorf("copy seed database: %w", err)
		}
	}
	if o.Prepare != nil {
		if err := o.Prepare(inst.DBPath); err != nil {
			return nil, fmt.Errorf("prepare database: %w", err)
		}
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	inst.Base = fmt.Sprintf("http://127.0.0.1:%d", port)
	// Like the smoke suite: drop every BINDERY_* variable of the outer shell,
	// then set exactly what this boot needs.
	env := make([]string, 0, len(os.Environ())+12)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BINDERY_") {
			env = append(env, kv)
		}
	}
	env = append(env,
		"BINDERY_PORT="+strconv.Itoa(port),
		"BINDERY_DB_PATH="+inst.DBPath,
		"BINDERY_DATA_DIR="+o.WorkDir,
		"BINDERY_API_KEY="+apiKey,
		"BINDERY_LIBRARY_DIR="+o.LibraryDir,
		"BINDERY_DOWNLOAD_DIR="+o.DownloadDir,
		"BINDERY_LOG_LEVEL="+cmp.Or(o.LogLevel, "info"),
		"BINDERY_TELEMETRY_DISABLED=true",
	)
	if o.ProxyURL != "" {
		env = append(env, "BINDERY_OUTBOUND_PROXY="+o.ProxyURL)
	}
	if o.Pprof {
		pp, err := freePort()
		if err != nil {
			return nil, err
		}
		inst.PprofAddr = fmt.Sprintf("127.0.0.1:%d", pp)
		env = append(env, "BINDERY_PERF_PPROF_ADDR="+inst.PprofAddr)
	}
	inst.log, err = os.Create(filepath.Join(o.WorkDir, "bindery.log"))
	if err != nil {
		return nil, err
	}
	// Not CommandContext: Stop sends SIGTERM so the binary drains and closes
	// its database, where a cancelled context would kill it outright.
	inst.cmd = exec.Command(o.Binary) //nolint:gosec,noctx // G204: the binary under test, chosen by the operator
	inst.cmd.Env, inst.cmd.Stdout, inst.cmd.Stderr = env, inst.log, inst.log
	start := time.Now()
	if err := inst.cmd.Start(); err != nil {
		_ = inst.log.Close()
		return nil, fmt.Errorf("start %s: %w", o.Binary, err)
	}
	go func() { _ = inst.cmd.Wait(); close(inst.done) }()

	hc := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if ok := healthOK(ctx, hc, inst.Base); ok {
			inst.Boot = time.Since(start)
			return inst, nil
		}
		select {
		case <-inst.done:
			return nil, fmt.Errorf("bindery exited during startup:\n%s", inst.logTail())
		case <-ctx.Done():
			inst.Stop()
			return nil, ctx.Err()
		case <-time.After(3 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			tail := inst.logTail()
			inst.Stop()
			return nil, fmt.Errorf("bindery not healthy after 5m:\n%s", tail)
		}
	}
}

func healthOK(ctx context.Context, hc *http.Client, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (i *Instance) logTail() string { return string(i.logEnd(4000)) }

// logEnd returns the last limit bytes of the binary's log.
func (i *Instance) logEnd(limit int) []byte {
	raw, err := os.ReadFile(filepath.Join(i.workDir, "bindery.log"))
	if err != nil {
		return []byte(err.Error())
	}
	if len(raw) > limit {
		raw = raw[len(raw)-limit:]
	}
	return raw
}

// Memory returns the resident set size and its high water mark in megabytes,
// read from /proc. Both are zero where /proc is not available.
func (i *Instance) Memory() (rssMB, peakMB float64) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", i.cmd.Process.Pid))
	if err != nil {
		return 0, 0
	}
	return procStatusKB(string(raw), "VmRSS:") / 1024, procStatusKB(string(raw), "VmHWM:") / 1024
}

func procStatusKB(status, key string) float64 {
	for line := range strings.SplitSeq(status, "\n") {
		if rest, ok := strings.CutPrefix(line, key); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				v, _ := strconv.ParseFloat(f[0], 64)
				return v
			}
		}
	}
	return 0
}

// cpuTicks is the user plus system CPU time the process has used, in clock
// ticks, or -1 where /proc is not available.
func (i *Instance) cpuTicks() int64 {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", i.cmd.Process.Pid))
	if err != nil {
		return -1
	}
	return parseStatTicks(string(raw))
}

func parseStatTicks(stat string) int64 {
	// The command name is in parentheses and may hold spaces; fields are
	// counted from the closing one. utime and stime are fields 14 and 15.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return -1
	}
	f := strings.Fields(stat[end+1:])
	if len(f) < 13 {
		return -1
	}
	u, _ := strconv.ParseInt(f[11], 10, 64)
	s, _ := strconv.ParseInt(f[12], 10, 64)
	return u + s
}

// WaitQuiet waits until the process has used almost no CPU for half a second,
// so one off startup work (backfills, recovery sweeps) is not measured as part
// of the first scenario. It returns how long that took, and gives up at limit.
func (i *Instance) WaitQuiet(ctx context.Context, limit time.Duration) time.Duration {
	start := time.Now()
	last, quiet := i.cpuTicks(), 0
	if last < 0 {
		return 0
	}
	for time.Since(start) < limit && ctx.Err() == nil {
		time.Sleep(250 * time.Millisecond)
		now := i.cpuTicks()
		if now-last <= 1 {
			quiet++
		} else {
			quiet = 0
		}
		last = now
		if quiet >= 2 {
			break
		}
	}
	return time.Since(start)
}

// DBSizeMB is the size of the database file plus its write ahead log.
func (i *Instance) DBSizeMB() float64 {
	var total int64
	for _, p := range []string{i.DBPath, i.DBPath + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return float64(total) / (1 << 20)
}

// Stop ends the process with SIGTERM, waits for it, and removes its work
// directory.
func (i *Instance) Stop() {
	_ = i.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-i.done:
	case <-time.After(20 * time.Second):
		_ = i.cmd.Process.Kill()
		<-i.done
	}
	_ = i.log.Close()
	if i.logCopy != "" {
		if err := os.MkdirAll(filepath.Dir(i.logCopy), 0o755); err == nil {
			_ = os.WriteFile(i.logCopy, i.logEnd(4<<20), 0o644)
		}
	}
	_ = os.RemoveAll(i.workDir)
}
