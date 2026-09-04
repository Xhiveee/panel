// Package fsmgr implements safe file operations inside an instance directory.
package fsmgr

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"panel/common/protocol"
)

// Limits for read/write payloads.
const (
	MaxReadBytes  = 2 * 1024 * 1024 // 2 MB
	MaxWriteBytes = 8 * 1024 * 1024 // 8 MB (decoded)
)

// ErrOutsideRoot is returned when a path escapes the instance root.
var ErrOutsideRoot = errors.New("path is outside the instance directory")

// Mgr serves file ops for one instance root directory.
type Mgr struct {
	Root string
}

// New creates a manager for root, which must be absolute.
func New(root string) (*Mgr, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Mgr{Root: abs}, nil
}

// Registry lazily creates a per-instance Mgr keyed by instance id, so file ops
// are always confined to the configured root for that instance.
type Registry struct {
	mu    sync.Mutex
	roots map[int64]string
	mgr   map[int64]*Mgr
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{roots: map[int64]string{}, mgr: map[int64]*Mgr{}}
}

// Bind records the root dir for an instance (called on start/create).
func (re *Registry) Bind(instanceID int64, root string) {
	re.mu.Lock()
	defer re.mu.Unlock()
	re.roots[instanceID] = root
	delete(re.mgr, instanceID) // re-create with the (possibly changed) root
}

// For returns the manager for an instance, creating it from the bound root.
func (re *Registry) For(instanceID int64, root string) (*Mgr, error) {
	re.mu.Lock()
	defer re.mu.Unlock()
	if root == "" {
		root = re.roots[instanceID]
	}
	m, ok := re.mgr[instanceID]
	if !ok {
		if root == "" {
			return nil, errors.New("no root bound for instance")
		}
		var err error
		m, err = New(root)
		if err != nil {
			return nil, err
		}
		re.mgr[instanceID] = m
		re.roots[instanceID] = root
	}
	return m, nil
}

// Drop removes an instance's manager (e.g. after an instance is deleted).
func (re *Registry) Drop(instanceID int64) {
	re.mu.Lock()
	defer re.mu.Unlock()
	delete(re.roots, instanceID)
	delete(re.mgr, instanceID)
}

// SafeJoin resolves rel inside root and refuses escapes.
func (m *Mgr) SafeJoin(rel string) (string, error) {
	if strings.Contains(rel, "\x00") {
		return "", errors.New("invalid path")
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return m.Root, nil
	}
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		return "", ErrOutsideRoot
	}
	// Windows drive-letter / UNC guard (filepath.IsAbs already catches most).
	if strings.Contains(clean, ":") {
		return "", ErrOutsideRoot
	}
	full := filepath.Join(m.Root, clean)
	relBack, err := filepath.Rel(m.Root, full)
	if err != nil {
		return "", ErrOutsideRoot
	}
	if relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return full, nil
}

// ensureRoot creates the root lazily so fresh instances are listable.
func (m *Mgr) ensureRoot() error {
	return os.MkdirAll(m.Root, 0o755)
}

// List returns directory entries.
func (m *Mgr) List(rel string) (protocol.FilesListResult, error) {
	if err := m.ensureRoot(); err != nil {
		return protocol.FilesListResult{}, err
	}
	full, err := m.SafeJoin(rel)
	if err != nil {
		return protocol.FilesListResult{}, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return protocol.FilesListResult{}, err
	}
	if !st.IsDir() {
		return protocol.FilesListResult{}, errors.New("not a directory")
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return protocol.FilesListResult{}, err
	}
	out := protocol.FilesListResult{Path: filepath.ToSlash(rel), Entries: []protocol.FileEntry{}}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue // raced/deleted
		}
		out.Entries = append(out.Entries, protocol.FileEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   info.IsDir(),
			ModTime: info.ModTime().UnixMilli(),
		})
	}
	return out, nil
}

// Read returns file content as base64 (capped at MaxReadBytes).
func (m *Mgr) Read(rel string) (protocol.FilesReadResult, error) {
	full, err := m.SafeJoin(rel)
	if err != nil {
		return protocol.FilesReadResult{}, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return protocol.FilesReadResult{}, err
	}
	if st.IsDir() {
		return protocol.FilesReadResult{}, errors.New("cannot read a directory")
	}
	f, err := os.Open(full)
	if err != nil {
		return protocol.FilesReadResult{}, err
	}
	defer f.Close()

	buf := make([]byte, MaxReadBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return protocol.FilesReadResult{}, err
	}
	res := protocol.FilesReadResult{}
	data := buf[:n]
	if n > MaxReadBytes {
		data = buf[:MaxReadBytes]
		res.Truncated = true
	}
	res.ContentB64 = base64.StdEncoding.EncodeToString(data)
	return res, nil
}

// Write decodes base64 content and atomically replaces the file.
func (m *Mgr) Write(rel, contentB64 string) error {
	if len(contentB64) > base64.StdEncoding.EncodedLen(MaxWriteBytes) {
		return fmt.Errorf("file too large (max %d bytes)", MaxWriteBytes)
	}
	data, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return errors.New("invalid base64 content")
	}
	if len(data) > MaxWriteBytes {
		return fmt.Errorf("file too large (max %d bytes)", MaxWriteBytes)
	}
	full, err := m.SafeJoin(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".panel-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Mkdir creates a directory (recursively) inside root.
func (m *Mgr) Mkdir(rel string) error {
	full, err := m.SafeJoin(rel)
	if err != nil {
		return err
	}
	if full == m.Root {
		return errors.New("cannot create the root")
	}
	return os.MkdirAll(full, 0o755)
}

// Delete removes a file or directory inside root; the root itself is protected.
func (m *Mgr) Delete(rel string) error {
	full, err := m.SafeJoin(rel)
	if err != nil {
		return err
	}
	if full == m.Root {
		return errors.New("refusing to delete the instance root")
	}
	if _, err := os.Stat(full); err != nil {
		return err
	}
	return os.RemoveAll(full)
}
