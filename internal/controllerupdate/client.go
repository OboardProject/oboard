package controllerupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	socketPath string
	http       *http.Client
}

func NewClient(socketPath string) *Client {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Timeout: 3 * time.Minute,
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			}},
		},
	}
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodGet, "/v1/status")
}

func (c *Client) Check(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/check")
}

func (c *Client) Prepare(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/prepare")
}

func (c *Client) Install(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/install")
}

func (c *Client) Cancel(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/cancel")
}

func (c *Client) SetChannel(ctx context.Context, channel string) (Status, error) {
	body, err := json.Marshal(ChannelRequest{Channel: channel})
	if err != nil {
		return Status{}, err
	}
	return c.callWithBody(ctx, http.MethodPost, "/v1/channel", body)
}

func (c *Client) call(ctx context.Context, method, path string) (Status, error) {
	return c.callWithBody(ctx, method, path, nil)
}

func (c *Client) callWithBody(ctx context.Context, method, path string, body []byte) (Status, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reader)
	if err != nil {
		return Status{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("controller updater unavailable: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Status{}, fmt.Errorf("read controller updater response: %w", err)
	}
	var result Status
	decodeErr := json.Unmarshal(payload, &result)
	if resp.StatusCode != http.StatusOK {
		statusErr := &UpdaterStatusError{Code: resp.StatusCode, Status: result}
		if decodeErr != nil {
			statusErr.Message = strings.TrimSpace(string(payload))
		}
		return result, statusErr
	}
	if decodeErr != nil {
		return Status{}, fmt.Errorf("decode controller updater response: %w", decodeErr)
	}
	return result, nil
}

// UpdaterStatusError reports a non-200 response from the controller updater.
type UpdaterStatusError struct {
	Code    int
	Status  Status
	Message string
}

func (e *UpdaterStatusError) Error() string {
	if strings.TrimSpace(e.Status.LastError) != "" {
		return "controller updater: " + e.Status.LastError
	}
	if strings.TrimSpace(e.Message) != "" {
		return fmt.Sprintf("controller updater returned HTTP %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("controller updater returned HTTP %d", e.Code)
}
