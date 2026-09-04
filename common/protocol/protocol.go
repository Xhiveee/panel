// Package protocol defines the master <-> agent wire format and typed ops.
package protocol

import (
	"encoding/json"
	"time"
)

// Message kinds.
const (
	KindRequest  = "request"
	KindResponse = "response"
	KindEvent    = "event"
)

// RequestTimeout bounds a single agent RPC from the master side.
const RequestTimeout = 15 * time.Second

// Ops that agents must implement. Anything else is rejected.
const (
	OpInstanceStart   = "instance.start"
	OpInstanceStop    = "instance.stop"
	OpInstanceRestart = "instance.restart"
	OpInstanceStatus  = "instance.status"
	OpConsoleAttach   = "console.attach"
	OpConsoleInput    = "console.input"
	OpConsoleDetach   = "console.detach"
	OpFilesList       = "files.list"
	OpFilesRead       = "files.read"
	OpFilesWrite      = "files.write"
	OpFilesMkdir      = "files.mkdir"
	OpFilesDelete     = "files.delete"
	OpMetricsSnapshot = "metrics.snapshot"
)

// Events emitted by agents without a matching request.
const (
	EventConsole        = "console"
	EventInstanceStatus = "instance.status"
	EventNodeMetrics    = "node.metrics"
)

var validOps = map[string]bool{
	OpInstanceStart: true, OpInstanceStop: true, OpInstanceRestart: true,
	OpInstanceStatus: true,
	OpConsoleAttach:  true, OpConsoleInput: true, OpConsoleDetach: true,
	OpFilesList: true, OpFilesRead: true, OpFilesWrite: true,
	OpFilesMkdir: true, OpFilesDelete: true,
	OpMetricsSnapshot: true,
}

// ValidOp reports whether op is a known request type.
func ValidOp(op string) bool { return validOps[op] }

// Message is the flat wire envelope used in both directions.
type Message struct {
	Kind   string          `json:"kind"`             // request | response | event
	ID     string          `json:"id,omitempty"`     // request/response correlation
	Op     string          `json:"op,omitempty"`     // request op
	Params json.RawMessage `json:"params,omitempty"` // request params
	OK     bool            `json:"ok,omitempty"`     // response status
	Error  string          `json:"error,omitempty"`  // response error text
	Result json.RawMessage `json:"result,omitempty"` // response result
	Event  string          `json:"event,omitempty"`  // event name
	Data   json.RawMessage `json:"data,omitempty"`   // event payload
}

// NewRequest builds a request message.
func NewRequest(id, op string, params any) Message {
	var p json.RawMessage
	if params != nil {
		p, _ = json.Marshal(params)
	}
	return Message{Kind: KindRequest, ID: id, Op: op, Params: p}
}

// NewResponse builds a response message.
func NewResponse(id string, ok bool, result any, errText string) Message {
	var r json.RawMessage
	if ok && result != nil {
		r, _ = json.Marshal(result)
	}
	return Message{Kind: KindResponse, ID: id, OK: ok, Result: r, Error: errText}
}

// NewEvent builds an event message.
func NewEvent(name string, data any) Message {
	d, _ := json.Marshal(data)
	return Message{Kind: KindEvent, Event: name, Data: d}
}

// ----- request params -----

// InstanceRef addresses a single instance.
type InstanceRef struct {
	InstanceID int64 `json:"instanceId"`
}

// InstanceSpec fully describes how to run an instance on the agent.
type InstanceSpec struct {
	InstanceID int64    `json:"instanceId"`
	Dir        string   `json:"dir"`
	Cmd        []string `json:"cmd"`
	StopCmd    string   `json:"stopCmd,omitempty"`
	Env        []string `json:"env,omitempty"`
}

// PathParams addresses a file or directory inside an instance dir.
type PathParams struct {
	InstanceID int64  `json:"instanceId"`
	Dir        string `json:"dir"` // agent-local instance root
	Path       string `json:"path"`
}

// FilesWriteParams writes (or overwrites) a file.
type FilesWriteParams struct {
	InstanceID int64  `json:"instanceId"`
	Dir        string `json:"dir"` // agent-local instance root
	Path       string `json:"path"`
	ContentB64 string `json:"contentB64"`
}

// ConsoleInputParams sends text to the instance stdin.
type ConsoleInputParams struct {
	InstanceID int64  `json:"instanceId"`
	Data       string `json:"data"`
}

// ----- results -----

// StatusResult is the runtime status of one instance.
type StatusResult struct {
	Running   bool  `json:"running"`
	PID       int   `json:"pid,omitempty"`
	StartedAt int64 `json:"startedAt,omitempty"` // unix ms, 0 when not running
	ExitCode  *int  `json:"exitCode,omitempty"`
}

// FilesListResult is a directory listing.
type FilesListResult struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// FileEntry is one row of a directory listing.
type FileEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
	ModTime int64  `json:"modTime"` // unix ms
}

// FilesReadResult returns file content as base64.
type FilesReadResult struct {
	ContentB64 string `json:"contentB64"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// ConsoleAttachResult includes recent output for instant scrollback.
type ConsoleAttachResult struct {
	Backlog string `json:"backlog"`
}

// MetricsSnapshot is a point-in-time node/instance metrics snapshot.
type MetricsSnapshot struct {
	CPU       float64          `json:"cpu"`
	MemTotal  uint64           `json:"memTotal"`
	MemUsed   uint64           `json:"memUsed"`
	Uptime    uint64           `json:"uptime"`
	Instances []InstanceMetric `json:"instances"`
}

// InstanceMetric is per-instance resource usage.
type InstanceMetric struct {
	InstanceID int64   `json:"instanceId"`
	CPU        float64 `json:"cpu"`
	RSS        uint64  `json:"rss"`
}

// ----- events -----

// ConsoleEvent carries raw console output.
type ConsoleEvent struct {
	InstanceID int64  `json:"instanceId"`
	Data       string `json:"data"`
}

// StatusEvent reports an instance state change.
type StatusEvent struct {
	InstanceID int64 `json:"instanceId"`
	Running    bool  `json:"running"`
	PID        int   `json:"pid,omitempty"`
	StartedAt  int64 `json:"startedAt,omitempty"`
	ExitCode   *int  `json:"exitCode,omitempty"`
}

// NodeMetricsEvent is the periodic node metrics push.
type NodeMetricsEvent struct {
	CPU       float64          `json:"cpu"`
	MemTotal  uint64           `json:"memTotal"`
	MemUsed   uint64           `json:"memUsed"`
	Uptime    uint64           `json:"uptime"`
	Instances []InstanceMetric `json:"instances"`
}
