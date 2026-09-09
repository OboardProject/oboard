package controllerupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/logging"
)

var errDownloadIdle = errors.New("controller package download made no progress before idle timeout")

func (s *Service) downloadControllerArchive(parent context.Context, release remoteRelease, artifact Artifact) (string, error) {
	if !hashPattern.MatchString(artifact.SHA256) || artifact.Size <= 0 || artifact.Size > 1<<30 {
		return "", errors.New("invalid controller download artifact")
	}
	ctx, cancel := context.WithTimeout(parent, s.config.DownloadTimeout)
	defer cancel()
	root, err := os.OpenRoot(s.config.WorkRoot)
	if err != nil {
		return "", err
	}
	defer root.Close()
	// The signed digest binds partial data to one immutable artifact, even on dev.
	name := "controller-" + artifact.SHA256 + ".part"
	directory, err := root.Open(".")
	if err != nil {
		return "", err
	}
	entries, err := directory.ReadDir(-1)
	_ = directory.Close()
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		other := entry.Name()
		digest := strings.TrimSuffix(strings.TrimPrefix(other, "controller-"), ".part")
		if other != name && other == "controller-"+digest+".part" && hashPattern.MatchString(digest) {
			if err := root.Remove(other); err != nil {
				return "", err
			}
		}
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := info.Size()
	if offset > artifact.Size {
		if err := file.Truncate(0); err != nil {
			return "", err
		}
		offset = 0
	}
	started := time.Now()
	initial := offset
	var transferred int64
	lastSaved := time.Time{}
	lastLogged := time.Time{}
	progress := DownloadProgress{TargetBuild: release.Manifest.Build, Bytes: offset, TotalBytes: artifact.Size}
	publish := func(force bool) {
		now := time.Now()
		if !force && now.Sub(lastSaved) < time.Second && !(progress.Bytes == initial && offset > initial) {
			return
		}
		progress.Bytes = offset
		progress.DurationMS = now.Sub(started).Milliseconds()
		progress.BytesPerSecond = int64(float64(transferred) / max(now.Sub(started).Seconds(), 0.001))
		s.mu.Lock()
		status := s.status
		copy := progress
		status.Download = &copy
		if err := s.saveStatus(status); err != nil {
			log.Printf("controller update download progress: %v", err)
		}
		s.mu.Unlock()
		if force || now.Sub(lastLogged) >= 10*time.Second {
			log.Printf("controller update download target_build=%s bytes=%d total=%d attempt=%d elapsed_ms=%d complete=%t", progress.TargetBuild, offset, artifact.Size, progress.Attempt, progress.DurationMS, progress.Complete)
			lastLogged = now
		}
		lastSaved = now
	}
	defer func() { publish(true) }()
	verify := func() error {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := verifyDownload(file, io.Discard, artifact); err != nil {
			_ = root.Remove(name)
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		progress.Complete = true
		return nil
	}
	if offset == artifact.Size {
		if err := verify(); err != nil {
			return "", err
		}
		return filepath.Join(s.config.WorkRoot, name), nil
	}
	// Preserve transport/auth/redirect policy but replace the metadata client's total timeout.
	client := *s.config.HTTPClient
	client.Timeout = 0
	var etag string
	for attempt := 1; attempt <= s.config.DownloadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if offset == artifact.Size {
			if err := verify(); err != nil {
				return "", err
			}
			return filepath.Join(s.config.WorkRoot, name), nil
		}
		progress.Attempt = attempt
		publish(true)
		retry, retryAfter, err := s.downloadAttempt(ctx, &client, release.Assets[artifact.Name], file, artifact.Size, &offset, &etag, func(n int) {
			transferred += int64(n)
			progress.LastProgressAt = time.Now().UTC().Format(time.RFC3339Nano)
			publish(false)
		})
		if err == nil {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if err := verify(); err != nil {
				return "", err
			}
			return filepath.Join(s.config.WorkRoot, name), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !retry {
			_ = root.Remove(name)
			return "", err
		}
		if attempt == s.config.DownloadAttempts {
			return "", fmt.Errorf("controller package download failed after %d attempts: %w", attempt, err)
		}
		delay := s.config.DownloadRetryDelay * time.Duration(1<<min(attempt-1, 5))
		delay += time.Duration(rand.Int64N(max(1, int64(delay/2))))
		delay = max(delay, retryAfter)
		log.Printf("controller update download retry attempt=%d bytes=%d delay=%s: %s", attempt, offset, delay, logging.Redact(err.Error()))
		if err := s.config.Wait(ctx, delay); err != nil {
			return "", err
		}
	}
	return "", errors.New("controller download attempts exhausted")
}

func (s *Service) downloadAttempt(parent context.Context, client *http.Client, address string, file *os.File, size int64, offset *int64, etag *string, progress func(int)) (bool, time.Duration, error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	timer := time.AfterFunc(s.config.DownloadIdleTimeout, func() { cancel(errDownloadIdle) })
	defer timer.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if *offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", *offset))
		if *etag != "" {
			req.Header.Set("If-Range", *etag)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return true, 0, cause
		}
		var network net.Error
		return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF), 0, errors.New(logging.Redact(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 && resp.StatusCode <= 599 {
		return true, downloadRetryAfter(resp.Header.Get("Retry-After")), fmt.Errorf("download controller package: HTTP %d", resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if err := file.Truncate(0); err != nil {
			return false, 0, err
		}
		*offset = 0
	case http.StatusPartialContent:
		expected := fmt.Sprintf("bytes %d-%d/%d", *offset, size-1, size)
		if resp.Header.Get("Content-Range") != expected {
			return false, 0, errors.New("controller package Content-Range does not match requested offset and signed size")
		}
	default:
		return false, 0, fmt.Errorf("download controller package: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size-*offset {
		return false, 0, errors.New("controller package Content-Length does not match signed size")
	}
	*etag = resp.Header.Get("ETag")
	if strings.HasPrefix(*etag, "W/") {
		*etag = ""
	}
	if _, err := file.Seek(*offset, io.SeekStart); err != nil {
		return false, 0, err
	}
	reader := io.LimitReader(resp.Body, size-*offset+1)
	buffer := make([]byte, 64<<10)
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			timer.Reset(s.config.DownloadIdleTimeout)
			if *offset+int64(n) > size {
				return false, 0, errors.New("controller package exceeds signed size")
			}
			written, err := file.Write(buffer[:n])
			*offset += int64(written)
			if err != nil {
				return false, 0, err
			}
			if written != n {
				return false, 0, io.ErrShortWrite
			}
			progress(written)
		}
		if cause := context.Cause(ctx); cause != nil {
			return true, 0, cause
		}
		if readErr != nil {
			if readErr == io.EOF && *offset == size {
				return false, 0, nil
			}
			if readErr == io.EOF {
				readErr = io.ErrUnexpectedEOF
			}
			return true, 0, readErr
		}
	}
}

func downloadRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(max(0, min(seconds, 60))) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(time.Minute, max(0, time.Until(at)))
	}
	return 0
}
