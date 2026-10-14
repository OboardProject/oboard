package ipquality

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"
)

type Result struct {
	Provider     string `json:"provider"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	CheckedAt    string `json:"checked_at"`
	ASN          string `json:"asn,omitempty"`
	Organization string `json:"organization,omitempty"`
	Country      string `json:"country,omitempty"`
	Proxy        *bool  `json:"proxy,omitempty"`
	VPN          *bool  `json:"vpn,omitempty"`
	Tor          *bool  `json:"tor,omitempty"`
	Hosting      *bool  `json:"hosting,omitempty"`
	Risk         *int   `json:"risk,omitempty"`
	Confidence   *int   `json:"confidence,omitempty"`
	Reports      *int   `json:"reports,omitempty"`
}
type Config struct{ IPinfo, Proxycheck, AbuseIPDB string }
type cacheEntry struct {
	result Result
	until  time.Time
}
type Client struct {
	Config Config
	HTTP   *http.Client
	mu     sync.Mutex
	cache  map[string]cacheEntry
	day    string
	used   map[string]int
}

func New(config Config) *Client {
	return &Client{Config: config, HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 8 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cache: map[string]cacheEntry{}, used: map[string]int{}}
}

func PublicIP(raw string) bool {
	ip, err := netip.ParseAddr(raw)
	return err == nil && pluginhttp.PublicIP(ip)
}

var asnPattern = regexp.MustCompile(`^AS[1-9][0-9]{0,9}$`)

func (c *Client) Lookup(ctx context.Context, provider, ip string) Result {
	at := time.Now()
	r := Result{Provider: provider, Status: "unavailable", CheckedAt: at.UTC().Format(time.RFC3339)}
	if !PublicIP(ip) {
		r.Error = "只允许公网 IP"
		return r
	}
	key := provider + ":" + ip
	c.mu.Lock()
	if cached, ok := c.cache[key]; ok && at.Before(cached.until) {
		c.mu.Unlock()
		return cached.result
	}
	c.mu.Unlock()
	token := ""
	endpoint := ""
	switch provider {
	case "ipinfo":
		token = c.Config.IPinfo
		endpoint = "https://api.ipinfo.io/lite/" + ip
	case "proxycheck":
		token = c.Config.Proxycheck
		endpoint = "https://proxycheck.io/v3/" + ip + "?tag=oboard-monitor"
		if token != "" {
			endpoint += "&key=" + url.QueryEscape(token)
		}
	case "abuseipdb":
		token = c.Config.AbuseIPDB
		endpoint = "https://api.abuseipdb.com/api/v2/check?ipAddress=" + url.QueryEscape(ip) + "&maxAgeInDays=30"
	default:
		r.Error = "未知数据源"
		return r
	}
	if token == "" && provider != "proxycheck" {
		r.Status = "not_configured"
		r.Error = "未配置 API Key"
		return r
	}
	c.mu.Lock()
	day := at.UTC().Format("2006-01-02")
	if c.day != day {
		c.day = day
		c.used = map[string]int{}
	}
	limit := 900
	if provider == "proxycheck" && token == "" {
		limit = 90
	}
	if provider == "ipinfo" {
		limit = 100000
	}
	if c.used[provider] >= limit {
		c.mu.Unlock()
		r.Status = "quota_exhausted"
		r.Error = "今日本地查询预算已用完"
		return r
	}
	c.used[provider]++
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		r.Error = "请求构造失败"
		return r
	}
	req.Header.Set("Accept", "application/json")
	if provider == "ipinfo" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if provider == "abuseipdb" {
		req.Header.Set("Key", token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		r.Error = "数据源请求失败"
		return r
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		r.Error = "数据源返回 HTTP " + strconv.Itoa(resp.StatusCode)
		if resp.StatusCode == 429 {
			r.Status = "quota_exhausted"
		}
		return r
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128*1024+1))
	if err != nil || len(body) > 128*1024 {
		r.Error = "数据源响应无效"
		return r
	}
	if err = decode(provider, ip, body, &r); err != nil {
		r.Error = "数据源响应缺少有效字段"
		return r
	}
	r.Status = "ok"
	c.mu.Lock()
	if len(c.cache) >= 4096 {
		c.cache = map[string]cacheEntry{}
	}
	ttl := 6 * time.Hour
	if provider == "ipinfo" {
		ttl = 24 * time.Hour
	}
	c.cache[key] = cacheEntry{r, at.Add(ttl)}
	c.mu.Unlock()
	return r
}
func decode(provider, ip string, body []byte, r *Result) error {
	switch provider {
	case "ipinfo":
		var v struct {
			ASN     string `json:"asn"`
			Name    string `json:"as_name"`
			Country string `json:"country_code"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return err
		}
		if !asnPattern.MatchString(v.ASN) {
			return errors.New("missing ASN")
		}
		r.ASN, r.Organization, r.Country = v.ASN, v.Name, v.Country
	case "proxycheck":
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(body, &envelope); err != nil {
			return err
		}
		var status string
		_ = json.Unmarshal(envelope["status"], &status)
		if status != "ok" && status != "warning" {
			return errors.New("provider failure")
		}
		var v struct {
			Network struct {
				ASN      string `json:"asn"`
				Provider string `json:"provider"`
			} `json:"network"`
			Location struct {
				Country string `json:"country_code"`
			} `json:"location"`
			Detections struct {
				Proxy      *bool `json:"proxy"`
				VPN        *bool `json:"vpn"`
				Tor        *bool `json:"tor"`
				Hosting    *bool `json:"hosting"`
				Risk       *int  `json:"risk"`
				Confidence *int  `json:"confidence"`
			} `json:"detections"`
		}
		if err := json.Unmarshal(envelope[ip], &v); err != nil {
			return err
		}
		if v.Detections.Proxy == nil {
			return errors.New("missing detection")
		}
		r.ASN, r.Organization, r.Country = v.Network.ASN, v.Network.Provider, v.Location.Country
		r.Proxy, r.VPN, r.Tor, r.Hosting, r.Risk, r.Confidence = v.Detections.Proxy, v.Detections.VPN, v.Detections.Tor, v.Detections.Hosting, v.Detections.Risk, v.Detections.Confidence
	case "abuseipdb":
		var v struct {
			Data struct {
				Score   *int   `json:"abuseConfidenceScore"`
				Reports *int   `json:"totalReports"`
				ISP     string `json:"isp"`
				Country string `json:"countryCode"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return err
		}
		if v.Data.Score == nil {
			return errors.New("missing score")
		}
		r.Confidence, r.Reports, r.Organization, r.Country = v.Data.Score, v.Data.Reports, v.Data.ISP, v.Data.Country
	}
	return nil
}

// ASNPath requires complete, single-ASN observations for every TTL. Missing
// hops and unresolved multipath are unknown, never evidence of a route change.
func ASNPath(trace model.NetworkTraceResult, lookup func(string) (string, error)) ([]string, error) {
	if !trace.Reached || trace.Truncated || len(trace.Hops) == 0 || len(trace.Hops) > model.TraceMaxHops {
		return nil, errors.New("路由未完整到达目标，本次不判定变化")
	}
	path := []string{}
	for i, hop := range trace.Hops {
		if hop.Hop != i+1 || len(hop.Addresses) == 0 || len(hop.Addresses) > model.TraceMaxQueriesPerHop {
			return nil, errors.New("路由存在未回应跳，本次不判定变化")
		}
		asn := ""
		private := false
		for _, address := range hop.Addresses {
			ip, err := netip.ParseAddr(address)
			if err != nil {
				return nil, errors.New("路由地址无效")
			}
			if ip.IsPrivate() {
				private = true
				continue
			}
			if !PublicIP(address) {
				return nil, errors.New("路由含无法归属的地址")
			}
			next, err := lookup(address)
			if err != nil || !asnPattern.MatchString(next) {
				return nil, errors.New("ASN 查询不完整，本次不判定变化")
			}
			if asn != "" && asn != next {
				return nil, errors.New("同一跳存在多个 ASN，本次不判定变化")
			}
			asn = next
		}
		if private && asn != "" {
			return nil, errors.New("同一跳存在混合路由，本次不判定变化")
		}
		if asn == "" {
			continue
		}
		if len(path) == 0 || path[len(path)-1] != asn {
			path = append(path, asn)
		}
	}
	if len(path) == 0 {
		return nil, errors.New("没有可比较的 ASN 路径")
	}
	return path, nil
}
