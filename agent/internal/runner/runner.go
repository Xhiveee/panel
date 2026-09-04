// Package runner starts/stops instance processes and streams their console.
package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"panel/common/protocol"
)

// ErrNotRunning is returned for console input / stop on a dead instance.
var ErrNotRunning = errors.New("instance is not running")

// ErrAlreadyRunning is returned when starting a running instance.
var ErrAlreadyRunning = errors.New("instance is already running")

// EmitFunc delivers protocol events upstream (to the master).
type EmitFunc func(msg protocol.Message)

// proc is one managed instance process.
type proc struct {
	spec      protocol.InstanceSpec
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	startedAt time.Time

	subsMu sync.Mutex
	subs   map[chan []byte]struct{}
	ring   *ring

	exited chan struct{}
	// exitCode is valid once exited is closed.
	exitCode atomic.Int32
	running  atomic.Bool
}

// Runner tracks local instance processes.
type Runner struct {
	mu    sync.Mutex
	opsMu sync.Mutex // serializes Start/Restart to close the Restart TOCTOU
	procs map[int64]*proc
	emit  EmitFunc

	baseDir string // canonical DataDir; instance Dirs must stay inside
}

// New creates a Runner emitting events through emit.
func New(emit EmitFunc) *Runner {
	return &Runner{procs: make(map[int64]*proc), emit: emit}
}

// SetBaseDir pins instance directories inside dir (canonicalized).
func (r *Runner) SetBaseDir(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.baseDir = canonicalDir(dir)
}

// Start launches the instance described by spec.
func (r *Runner) Start(spec protocol.InstanceSpec) (protocol.StatusResult, error) {
	r.opsMu.Lock()
	defer r.opsMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.procs[spec.InstanceID]; ok && p.running.Load() {
		return p.status(), ErrAlreadyRunning
	}
	if err := validateSpec(r.baseDir, spec); err != nil {
		return protocol.StatusResult{}, err
	}
	if err := os.MkdirAll(spec.Dir, 0o755); err != nil {
		return protocol.StatusResult{}, fmt.Errorf("create dir: %w", err)
	}

	cmd := exec.Command(spec.Cmd[0], spec.Cmd[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, spec.Env...)

	p := &proc{spec: spec, subs: make(map[chan []byte]struct{}), ring: newRing(256 * 1024)}
	p.exited = make(chan struct{})

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return protocol.StatusResult{}, err
	}
	p.stdin = stdin

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return protocol.StatusResult{}, err
	}
	cmd.Stderr = cmd.Stdout // merge stderr into the console stream

	if err := setProcessGroup(cmd); err != nil {
		return protocol.StatusResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return protocol.StatusResult{}, fmt.Errorf("start: %w", err)
	}
	p.cmd = cmd
	p.startedAt = time.Now()
	p.running.Store(true)
	r.procs[spec.InstanceID] = p

	// Console pump: process output -> subscribers + backlog.
	go func() {
		rd := bufio.NewReaderSize(stdout, 64*1024)
		buf := make([]byte, 16*1024)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				p.pushConsole(chunk)
			}
			if err != nil {
				return
			}
		}
	}()

	pid := p.cmd.Process.Pid
	// Waiter: detects exit and emits status.
	go func() {
		err := cmd.Wait()
		p.running.Store(false)
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		p.exitCode.Store(int32(code))
		close(p.exited)

		r.removeIfCurrent(spec.InstanceID, p)
		if r.emit != nil {
			r.emit(protocol.NewEvent(protocol.EventInstanceStatus, protocol.StatusEvent{
				InstanceID: spec.InstanceID, Running: false, ExitCode: intPtr(code),
			}))
		}
	}()

	if r.emit != nil {
		r.emit(protocol.NewEvent(protocol.EventInstanceStatus, protocol.StatusEvent{
			InstanceID: spec.InstanceID, Running: true, PID: pid, StartedAt: p.startedAt.UnixMilli(),
		}))
	}
	return protocol.StatusResult{Running: true, PID: pid, StartedAt: p.startedAt.UnixMilli()}, nil
}

func (r *Runner) removeIfCurrent(id int64, p *proc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.procs[id]; ok && cur == p {
		delete(r.procs, id)
	}
}

func intPtr(i int) *int { return &i }

// Stop terminates an instance: stop command first, then kill.
func (r *Runner) Stop(instanceID int64) (protocol.StatusResult, error) {
	p, err := r.get(instanceID)
	if err != nil {
		return protocol.StatusResult{}, err
	}
	if p.spec.StopCmd != "" {
		_, _ = io.WriteString(p.stdin, p.spec.StopCmd+"\n")
	}
	select {
	case <-p.exited:
	case <-time.After(20 * time.Second):
		_ = p.kill()
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			return protocol.StatusResult{}, errors.New("process refuses to die")
		}
	}
	return protocol.StatusResult{Running: false}, nil
}

// Restart stops (if needed) and starts the instance with its last spec.
// Start/Restart are serialized via opsMu so concurrent starts cannot
// duplicate the process.
func (r *Runner) Restart(instanceID int64) (protocol.StatusResult, error) {
	r.opsMu.Lock()
	defer r.opsMu.Unlock()
	r.mu.Lock()
	p, ok := r.procs[instanceID]
	spec := protocol.InstanceSpec{}
	if ok {
		spec = p.spec
	}
	r.mu.Unlock()
	if !ok {
		return protocol.StatusResult{}, ErrNotRunning
	}
	if p.running.Load() {
		if _, err := r.Stop(instanceID); err != nil {
			return protocol.StatusResult{}, err
		}
	}
	return r.Start(spec)
}

func (r *Runner) get(id int64) (*proc, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.procs[id]
	if !ok || !p.running.Load() {
		return nil, ErrNotRunning
	}
	return p, nil
}

// Status reports the instance state.
func (r *Runner) Status(instanceID int64) protocol.StatusResult {
	r.mu.Lock()
	p, ok := r.procs[instanceID]
	r.mu.Unlock()
	if !ok {
		return protocol.StatusResult{}
	}
	return p.status()
}

func (p *proc) status() protocol.StatusResult {
	if !p.running.Load() {
		code := int(p.exitCode.Load())
		return protocol.StatusResult{Running: false, ExitCode: &code}
	}
	return protocol.StatusResult{
		Running:   true,
		PID:       p.cmd.Process.Pid,
		StartedAt: p.startedAt.UnixMilli(),
	}
}

func (p *proc) kill() error {
	return killProcess(p.cmd)
}

// ---------- console attach/input ----------

// Attach subscribes to the console stream, returning backlog + live chunks.
func (r *Runner) Attach(instanceID int64) (<-chan []byte, string, func(), error) {
	r.mu.Lock()
	p, ok := r.procs[instanceID]
	r.mu.Unlock()
	if !ok {
		return nil, "", nil, ErrNotRunning
	}
	ch := make(chan []byte, 512)
	p.subsMu.Lock()
	p.subs[ch] = struct{}{}
	backlog := p.ring.snapshot()
	p.subsMu.Unlock()
	cancel := func() {
		p.subsMu.Lock()
		delete(p.subs, ch)
		p.subsMu.Unlock()
		close(ch)
	}
	return ch, backlog, cancel, nil
}

// Detach is a no-op with subscriber-scoped cancels, kept for protocol symmetry.
func (r *Runner) Detach(instanceID int64) {}

// Input writes raw data to the instance stdin.
func (r *Runner) Input(instanceID int64, data string) error {
	if len(data) > 8192 {
		return errors.New("input too large")
	}
	p, err := r.get(instanceID)
	if err != nil {
		return err
	}
	_, err = io.WriteString(p.stdin, data)
	return err
}

// RunningInstanceIDs returns ids with live processes (for metrics).
func (r *Runner) RunningInstanceIDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int64
	for id, p := range r.procs {
		if p.running.Load() {
			out = append(out, id)
		}
	}
	return out
}

// PIDOf returns the pid of a running instance or 0.
func (r *Runner) PIDOf(instanceID int64) int {
	r.mu.Lock()
	p, ok := r.procs[instanceID]
	r.mu.Unlock()
	if !ok || !p.running.Load() {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *proc) pushConsole(chunk []byte) {
	p.ring.append(chunk)
	p.subsMu.Lock()
	for ch := range p.subs {
		select {
		case ch <- chunk:
		default: // slow subscriber: drop
		}
	}
	p.subsMu.Unlock()
}

// ---------- ring buffer for console backlog ----------

type ring struct {
	mu   sync.Mutex
	data []byte
	cap  int
}

func newRing(capacity int) *ring {
	return &ring{cap: capacity}
}

func (r *ring) append(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, b...)
	if len(r.data) > r.cap {
		r.data = r.data[len(r.data)-r.cap:]
	}
}

func (r *ring) snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.data)
}

// ---------- platform plumbing ----------

// blockedEnvPrefixes are never inherited from a remote spec: they allow
// library/shell hijack even with an innocent-looking Cmd.
var blockedEnvPrefixes = []string{"LD_", "DYLD_", "PATH=", "PYTHONPATH=", "RUBYLIB=", "PERL5LIB=", "JAVA_TOOL_OPTIONS=", "JDK_JAVA_OPTIONS="}

func canonicalDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return abs
}

// insideBase reports whether target stays inside base (both canonical).
func insideBase(base, target string) bool {
	if base == "" {
		return true // unpinned (tests / old wiring): validated elsewhere
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// validateSpec rejects dangerous instance specs before any MkdirAll/exec.
func validateSpec(baseDir string, spec protocol.InstanceSpec) error {
	if len(spec.Cmd) == 0 || len(spec.Cmd) > 32 {
		return errors.New("empty command")
	}
	for _, c := range spec.Cmd {
		if strings.ContainsRune(c, 0) || len(c) > 4096 {
			return errors.New("invalid command argument")
		}
	}
	if strings.ContainsRune(spec.Dir, 0) || len(spec.Dir) == 0 || len(spec.Dir) > 1024 {
		return errors.New("invalid dir")
	}
	if baseDir != "" {
		abs, err := filepath.Abs(spec.Dir)
		if err != nil {
			return errors.New("invalid dir")
		}
		if !insideBase(baseDir, abs) {
			return errors.New("dir is outside the agent data dir")
		}
	}
	if len(spec.Env) > 64 {
		return errors.New("too many env entries")
	}
	for _, kv := range spec.Env {
		if strings.ContainsRune(kv, 0) || len(kv) > 8192 {
			return errors.New("invalid env entry")
		}
		up := strings.ToUpper(kv)
		for _, b := range blockedEnvPrefixes {
			if strings.HasPrefix(up, b) {
				return fmt.Errorf("blocked env entry: %s", b)
			}
		}
	}
	if len(spec.StopCmd) > 8192 || strings.ContainsRune(spec.StopCmd, 0) {
		return errors.New("invalid stop command")
	}
	return nil
}
