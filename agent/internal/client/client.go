// Package client maintains the websocket session with the master and
// dispatches typed operations to local services.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"panel/agent/internal/config"
	"panel/agent/internal/fsmgr"
	"panel/agent/internal/metrics"
	"panel/agent/internal/runner"
	"panel/common/protocol"
)

// Services bundles local capability providers.
type Services struct {
	Runner  *runner.Runner
	Files   *fsmgr.Registry
	Metrics *metrics.Collector
}

// Client is the master connection manager.
type Client struct {
	cfg  *config.Config
	svcs Services

	conn    *websocket.Conn
	writeMu sync.Mutex

	mu            sync.Mutex
	consoleCancel map[int64]func()
}

// New creates a client.
func New(cfg *config.Config, r *runner.Runner, fr *fsmgr.Registry, mc *metrics.Collector) *Client {
	return &Client{
		cfg: cfg,
		svcs: Services{
			Runner: r, Files: fr, Metrics: mc,
		},
		consoleCancel: map[int64]func(){},
	}
}

// Run connects to the master, reconnecting with backoff until ctx ends.
func (c *Client) Run(ctx context.Context) error {
	backoff := 1 * time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := c.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		// session only returns on error: reset backoff on clean dials is
		// handled inside session; here always back off.
		slog.Warn("agent connection lost, reconnecting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// session runs one connected session until an error occurs.
func (c *Client) session(ctx context.Context) error {
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	defer dcancel()
	conn, _, err := websocket.Dial(dctx, c.cfg.MasterURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + c.cfg.Token}},
	})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	c.conn = conn
	conn.SetReadLimit(4 << 20) // bound a single master frame (DoS guard)
	slog.Info("connected to master", "url", c.cfg.MasterURL)

	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	defer c.shutdown()

	// Periodic metrics push doubles as a keepalive.
	go func() {
		<-sctx.Done()
		c.svcs.Metrics.Stop()
	}()
	c.svcs.Metrics.Start()

	// Read loop: master requests.
	for {
		_, data, err := conn.Read(sctx)
		if err != nil {
			return err
		}
		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("bad message from master", "err", err)
			continue
		}
		if msg.Kind != protocol.KindRequest {
			continue
		}
		if !protocol.ValidOp(msg.Op) {
			c.reply(sctx, protocol.NewResponse(msg.ID, false, nil, "invalid op"))
			continue
		}
		go c.handle(sctx, msg)
	}
}

// shutdown flushes pending state when the connection drops.
func (c *Client) shutdown() {
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

// Emit sends an event message to the master. Safe before a connection exists
// (events are simply dropped until the session is live).
func (c *Client) Emit(msg protocol.Message) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, mustJSON(msg)); err != nil {
		slog.Debug("event write failed", "err", err)
	}
}

// reply writes a response message.
func (c *Client) reply(ctx context.Context, msg protocol.Message) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.conn.Write(wctx, websocket.MessageText, mustJSON(msg)); err != nil {
		slog.Debug("response write failed", "err", err)
	}
}

// handle executes one request and replies.
func (c *Client) handle(ctx context.Context, msg protocol.Message) {
	result, err := c.dispatch(ctx, msg.Op, msg.Params)
	if err != nil {
		c.reply(ctx, protocol.NewResponse(msg.ID, false, nil, err.Error()))
		return
	}
	c.reply(ctx, protocol.NewResponse(msg.ID, true, result, ""))
}

func (c *Client) dispatch(ctx context.Context, op string, params json.RawMessage) (any, error) {
	switch op {
	case protocol.OpInstanceStart:
		var spec protocol.InstanceSpec
		if err := decode(params, &spec); err != nil {
			return nil, err
		}
		res, err := c.svcs.Runner.Start(spec)
		if err != nil {
			return nil, err
		}
		c.svcs.Files.Bind(spec.InstanceID, spec.Dir)
		return res, nil

	case protocol.OpInstanceStop:
		var ref protocol.InstanceRef
		if err := decode(params, &ref); err != nil {
			return nil, err
		}
		res, err := c.svcs.Runner.Stop(ref.InstanceID)
		if err != nil {
			return nil, err
		}
		return res, nil

	case protocol.OpInstanceRestart:
		var spec protocol.InstanceSpec
		if err := decode(params, &spec); err != nil {
			return nil, err
		}
		res, err := c.svcs.Runner.Restart(spec.InstanceID)
		if err != nil {
			return nil, err
		}
		return res, nil

	case protocol.OpInstanceStatus:
		var ref protocol.InstanceRef
		if err := decode(params, &ref); err != nil {
			return nil, err
		}
		return c.svcs.Runner.Status(ref.InstanceID), nil

	case protocol.OpConsoleAttach:
		var ref protocol.InstanceRef
		if err := decode(params, &ref); err != nil {
			return nil, err
		}
		ch, backlog, cancel, err := c.svcs.Runner.Attach(ref.InstanceID)
		if err != nil {
			return nil, err
		}
		// Stream live chunks to the master as console events, then return backlog.
		go func() {
			for chunk := range ch {
				c.Emit(protocol.NewEvent(protocol.EventConsole, protocol.ConsoleEvent{
					InstanceID: ref.InstanceID, Data: string(chunk),
				}))
			}
		}()
		// cancel is held by the master's console WS when it detaches; the
		// goroutine above stops when the channel is closed by that cancel.
		c.mu.Lock()
		if old, ok := c.consoleCancel[ref.InstanceID]; ok {
			old() // do not leak the previous stream on re-attach
			delete(c.consoleCancel, ref.InstanceID)
		}
		c.consoleCancel[ref.InstanceID] = cancel
		c.mu.Unlock()
		return protocol.ConsoleAttachResult{Backlog: backlog}, nil

	case protocol.OpConsoleInput:
		var in protocol.ConsoleInputParams
		if err := decode(params, &in); err != nil {
			return nil, err
		}
		if len(in.Data) > 8192 {
			return nil, errors.New("input too large")
		}
		return nil, c.svcs.Runner.Input(in.InstanceID, in.Data)

	case protocol.OpConsoleDetach:
		var ref protocol.InstanceRef
		if err := decode(params, &ref); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if cancel, ok := c.consoleCancel[ref.InstanceID]; ok {
			cancel()
			delete(c.consoleCancel, ref.InstanceID)
		}
		c.mu.Unlock()
		c.svcs.Runner.Detach(ref.InstanceID)
		return nil, nil

	case protocol.OpFilesList:
		var p protocol.PathParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		m, err := c.svcs.Files.For(p.InstanceID, p.Dir)
		if err != nil {
			return nil, err
		}
		return m.List(p.Path)

	case protocol.OpFilesRead:
		var p protocol.PathParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		m, err := c.svcs.Files.For(p.InstanceID, p.Dir)
		if err != nil {
			return nil, err
		}
		return m.Read(p.Path)

	case protocol.OpFilesWrite:
		var p protocol.FilesWriteParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		m, err := c.svcs.Files.For(p.InstanceID, p.Dir)
		if err != nil {
			return nil, err
		}
		return nil, m.Write(p.Path, p.ContentB64)

	case protocol.OpFilesMkdir:
		var p protocol.PathParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		m, err := c.svcs.Files.For(p.InstanceID, p.Dir)
		if err != nil {
			return nil, err
		}
		return nil, m.Mkdir(p.Path)

	case protocol.OpFilesDelete:
		var p protocol.PathParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		m, err := c.svcs.Files.For(p.InstanceID, p.Dir)
		if err != nil {
			return nil, err
		}
		return nil, m.Delete(p.Path)

	case protocol.OpMetricsSnapshot:
		return c.svcs.Metrics.Snapshot(), nil

	default:
		return nil, errors.New("unsupported op")
	}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return errors.New("missing params")
	}
	return json.Unmarshal(raw, v)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
