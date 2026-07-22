package logging

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxBytes = int64(32 << 20)
	DefaultBackups  = 5
)

type Config struct {
	MaxBytes int64
	Backups  int
}

type FileInfo struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	Modified  time.Time `json:"modified_at"`
}

type Snapshot struct {
	Content        string     `json:"content"`
	LineCount      int        `json:"line_count"`
	Files          []FileInfo `json:"files"`
	TotalSizeBytes int64      `json:"total_size_bytes"`
	MaxSizeBytes   int64      `json:"max_size_bytes"`
	Backups        int        `json:"backups"`
}

type Manager struct {
	mu     sync.Mutex
	path   string
	config Config
	file   *os.File
	size   int64
	closed bool
}

var sensitiveValue = regexp.MustCompile(`(?i)(authorization|agent_token|api_token|token|password|private_key|secret)(["'=:\s]+)([^\s",}]+)`)
var bearerValue = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)

func New(path string, config Config) (*Manager, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("log path is empty")
	}
	config = normalizeConfig(config)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	m := &Manager{path: path, config: config}
	if err := m.openLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

func normalizeConfig(config Config) Config {
	if config.MaxBytes <= 0 {
		config.MaxBytes = DefaultMaxBytes
	}
	if config.Backups < 0 {
		config.Backups = 0
	}
	if config.Backups > 20 {
		config.Backups = 20
	}
	return config
}

func (m *Manager) Path() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.path
}

func (m *Manager) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, os.ErrClosed
	}
	clean := redact(p)
	if int64(len(clean)) > m.config.MaxBytes {
		clean = clean[int64(len(clean))-m.config.MaxBytes:]
	}
	if m.size > 0 && m.size+int64(len(clean)) > m.config.MaxBytes {
		if err := m.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := m.file.Write(clean)
	m.size += int64(n)
	if err != nil {
		return n, err
	}
	// Report the original byte count so io.MultiWriter accepts redacted writes.
	return len(p), nil
}

func redact(p []byte) []byte {
	clean := bearerValue.ReplaceAll(p, []byte("Bearer [REDACTED]"))
	clean = sensitiveValue.ReplaceAll(clean, []byte("$1$2[REDACTED]"))
	return clean
}

func (m *Manager) Configure(config Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return os.ErrClosed
	}
	m.config = normalizeConfig(config)
	if m.size > m.config.MaxBytes {
		return m.rotateLocked()
	}
	return m.removeExcessBackupsLocked()
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.config
}

func (m *Manager) Rotate() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return os.ErrClosed
	}
	return m.rotateLocked()
}

func (m *Manager) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return os.ErrClosed
	}
	if err := m.file.Truncate(0); err != nil {
		return err
	}
	if _, err := m.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	m.size = 0
	for i := 1; i <= 20; i++ {
		if err := os.Remove(m.backupPath(i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *Manager) Snapshot(limit int, query string) (Snapshot, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Snapshot{}, os.ErrClosed
	}
	if err := m.file.Sync(); err != nil {
		return Snapshot{}, err
	}
	files, total, err := m.filesLocked()
	if err != nil {
		return Snapshot{}, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	selected := make([]string, 0, limit)
	scanLimit := limit
	if needle != "" {
		scanLimit = limit * 20
		if scanLimit < 5000 {
			scanLimit = 5000
		}
	}
	for i := 0; i <= m.config.Backups && len(selected) < limit; i++ {
		path := m.path
		if i > 0 {
			path = m.backupPath(i)
		}
		lines, readErr := tailFileLines(path, scanLimit)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return Snapshot{}, readErr
		}
		for j := len(lines) - 1; j >= 0 && len(selected) < limit; j-- {
			if needle == "" || strings.Contains(strings.ToLower(lines[j]), needle) {
				selected = append(selected, lines[j])
			}
		}
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	content := strings.Join(selected, "\n")
	if content != "" {
		content += "\n"
	}
	return Snapshot{Content: content, LineCount: len(selected), Files: files, TotalSizeBytes: total, MaxSizeBytes: m.config.MaxBytes, Backups: m.config.Backups}, nil
}

func (m *Manager) WriteZIP(dst io.Writer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return os.ErrClosed
	}
	if err := m.file.Sync(); err != nil {
		return err
	}
	zw := zip.NewWriter(dst)
	paths := make([]string, 0, m.config.Backups+1)
	for i := m.config.Backups; i >= 1; i-- {
		paths = append(paths, m.backupPath(i))
	}
	paths = append(paths, m.path)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.Base(path)
		header.Method = zip.Deflate
		entry, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		// #nosec G304 -- paths come only from this manager's configured log path and numbered backups.
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return zw.Close()
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.file.Close()
}

func (m *Manager) openLocked() error {
	file, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	m.file = file
	m.size = info.Size()
	return nil
}

func (m *Manager) rotateLocked() error {
	if err := m.file.Close(); err != nil {
		return err
	}
	m.file = nil
	reopenAfterError := func(operationErr error) error {
		m.size = 0
		if reopenErr := m.openLocked(); reopenErr != nil {
			return fmt.Errorf("%v; reopen log: %w", operationErr, reopenErr)
		}
		return operationErr
	}
	if m.config.Backups > 0 {
		_ = os.Remove(m.backupPath(m.config.Backups))
		for i := m.config.Backups - 1; i >= 1; i-- {
			if err := os.Rename(m.backupPath(i), m.backupPath(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return reopenAfterError(err)
			}
		}
		if err := os.Rename(m.path, m.backupPath(1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return reopenAfterError(err)
		}
	} else if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return reopenAfterError(err)
	}
	m.size = 0
	return m.openLocked()
}

func (m *Manager) removeExcessBackupsLocked() error {
	for i := m.config.Backups + 1; i <= 20; i++ {
		if err := os.Remove(m.backupPath(i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (m *Manager) backupPath(index int) string {
	return fmt.Sprintf("%s.%d", m.path, index)
}

func (m *Manager) filesLocked() ([]FileInfo, int64, error) {
	files := []FileInfo{}
	var total int64
	for i := 0; i <= m.config.Backups; i++ {
		path := m.path
		if i > 0 {
			path = m.backupPath(i)
		}
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, 0, err
		}
		files = append(files, FileInfo{Name: filepath.Base(path), SizeBytes: info.Size(), Modified: info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, total, nil
}

func tailFileLines(path string, maxLines int) ([]string, error) {
	if maxLines <= 0 {
		return nil, nil
	}
	// #nosec G304 -- callers pass this manager's configured log path or a numbered backup path.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	const blockSize int64 = 32 << 10
	position := info.Size()
	buffer := []byte{}
	for position > 0 && bytes.Count(buffer, []byte{'\n'}) <= maxLines {
		readSize := blockSize
		if position < readSize {
			readSize = position
		}
		position -= readSize
		block := make([]byte, readSize)
		if _, err := file.ReadAt(block, position); err != nil && err != io.EOF {
			return nil, err
		}
		buffer = append(block, buffer...)
	}
	text := strings.TrimRight(string(buffer), "\r\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines, nil
}
