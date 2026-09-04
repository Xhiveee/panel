// Package runner starts/stops instance processes and streams their console.
package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	procs map[int64]*proc
	emit  EmitFunc
}

// New creates a Runner emitting events through emit.
func New(emit EmitFunc) *Runner {
	return &Runner{procs: make(map[int64]*proc), emit: emit}
}

// Start launches the instance described by spec.
func (r *Runner) Start(spec protocol.InstanceSpec) (protocol.StatusResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.procs[spec.InstanceID]; ok && p.running.Load() {
		return p.status(), ErrAlreadyRunning
	}
	if len(spec.Cmd) == 0 {
		return protocol.StatusResult{}, errors.New("empty command")
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
func (r *Runner) Restart(instanceID int64) (protocol.StatusResult, error) {
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
