package controllerupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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

func (c *Client) Install(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/install")
}

func (c *Client) Cancel(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/cancel")
}

func (c *Client) call(ctx context.Context, method, path string) (Status, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, nil)
	if err != nil {
		return Status{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("controller updater unavailable: %w", err)
	}
	defer resp.Body.Close()
	var result Status
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Status{}, fmt.Errorf("decode controller updater response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if result.LastError != "" {
			return result, fmt.Errorf("controller updater: %s", result.LastError)
		}
		return result, fmt.Errorf("controller updater returned HTTP %d", resp.StatusCode)
	}
	return result, nil
}
