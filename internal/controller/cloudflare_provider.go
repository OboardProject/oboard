package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultCloudflareAPIBase = "https://api.cloudflare.com/client/v4"

type cloudflareClient struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

type cloudflareTokenStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type cloudflareZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Comment string `json:"comment"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

type cloudflareEnvelope struct {
	Success bool              `json:"success"`
	Errors  []cloudflareError `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newCloudflareClient(token, baseURL string) *cloudflareClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultCloudflareAPIBase
	}
	return &cloudflareClient{token: strings.TrimSpace(token), baseURL: strings.TrimRight(baseURL, "/"), httpClient: &http.Client{Timeout: 20 * time.Second}}
}

func (c *cloudflareClient) verifyToken(ctx context.Context) (cloudflareTokenStatus, error) {
	if strings.TrimSpace(c.token) == "" {
		return cloudflareTokenStatus{}, errors.New("未配置 Cloudflare API Token")
	}
	var result cloudflareTokenStatus
	if err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, nil, &result); err != nil {
		return cloudflareTokenStatus{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(result.Status), "active") {
		return cloudflareTokenStatus{}, fmt.Errorf("cloudflare token status is %s", result.Status)
	}
	return result, nil
}

func (c *cloudflareClient) findZone(ctx context.Context, domain string) (cloudflareZone, error) {
	domain = normalizeDomainName(domain)
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return cloudflareZone{}, fmt.Errorf("invalid domain %q", domain)
	}
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		query := url.Values{"name": []string{candidate}, "per_page": []string{"1"}}
		var zones []cloudflareZone
		if err := c.do(ctx, http.MethodGet, "/zones", query, nil, &zones); err != nil {
			return cloudflareZone{}, err
		}
		if len(zones) > 0 && zones[0].ID != "" {
			return zones[0], nil
		}
	}
	return cloudflareZone{}, fmt.Errorf("no Cloudflare zone found for %s", domain)
}

func (c *cloudflareClient) listDNSRecords(ctx context.Context, zone cloudflareZone) ([]cloudflareDNSRecord, error) {
	query := url.Values{"per_page": []string{"5000"}}
	var records []cloudflareDNSRecord
	if err := c.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zone.ID)+"/dns_records", query, nil, &records); err != nil {
		return nil, err
	}
	return records, nil
}

func (c *cloudflareClient) deleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	return c.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, nil, nil)
}

func (c *cloudflareClient) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope cloudflareEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("cloudflare API returned HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
		return fmt.Errorf("cloudflare API request failed: %s", cloudflareErrorText(envelope.Errors, resp.StatusCode))
	}
	if out == nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

func cloudflareErrorText(items []cloudflareError, status int) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Message) == "" {
			continue
		}
		if item.Code != 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.Code, item.Message))
		} else {
			parts = append(parts, item.Message)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return strings.Join(parts, "; ")
}

func normalizeDomainName(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

func isDNSDomainName(raw string) bool {
	domain := normalizeDomainName(raw)
	if len(domain) < 3 || len(domain) > 253 || strings.Contains(domain, "..") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
