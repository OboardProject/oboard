// Package nexttrace implements the official NextTrace API wire protocol for
// Controller-side route attribution fallback. It does not execute the NextTrace CLI.
package nexttrace

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/OboardProject/oboard/internal/ipdata"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/gorilla/websocket"
	"io"
	"math/big"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const Provider = "NextTrace-API"

// SourceController marks hop geography the Controller filled itself.
const SourceController = "nexttrace_controller"
const userAgent = "OBoard-Controller (NextTrace API client)"

type entry struct {
	geo     model.NetworkTraceGeography
	expires time.Time
}
type Client struct {
	mu                            sync.Mutex
	http                          *http.Client
	dialer                        *websocket.Dialer
	endpoint, wsEndpoint, v4Token string
	token                         string
	tokenExpires                  time.Time
	cooldown                      time.Time
	cache                         map[string]entry
}

func New(clock func() time.Time, v4Token string) *Client {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, Time: clock}
	return &Client{http: &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: tlsConfig, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 * 1024}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, dialer: &websocket.Dialer{TLSClientConfig: tlsConfig, HandshakeTimeout: 5 * time.Second, Proxy: nil}, endpoint: "https://api.nxtrace.org", wsEndpoint: "wss://api.nxtrace.org/v3/ipGeoWs", v4Token: strings.TrimSpace(v4Token), cache: map[string]entry{}}
}

// SetToken switches between the free v3 PoW flow (empty) and the
// token-authenticated v4 endpoint. A change clears cooldown so a fixed token
// is used on the next query.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if token = strings.TrimSpace(token); token != c.v4Token {
		c.v4Token, c.cooldown = token, time.Time{}
	}
}

// Fill queries only public hop addresses that carry no attribution yet and
// appends NextTrace geography for them. It never touches hops the Agent
// already attributed, and returns the first query error.
func (c *Client) Fill(ctx context.Context, trace *model.NetworkTraceResult) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(trace.Hops) > model.TraceMaxHops {
		return errors.New("路由跳数超出限制")
	}
	pending := []string{}
	seen := map[string]bool{}
	found := map[string]model.NetworkTraceGeography{}
	now := time.Now()
	for _, hop := range trace.Hops {
		have := map[string]bool{}
		for _, geo := range hop.Geography {
			if geo.ASN != "" {
				have[geo.Address] = true
			}
		}
		for _, raw := range hop.Addresses {
			if _, err := netip.ParseAddr(raw); err != nil || !ipdata.PublicIP(raw) || seen[raw] || have[raw] {
				continue
			}
			seen[raw] = true
			if cached, ok := c.cache[raw]; ok && now.Before(cached.expires) {
				found[raw] = cached.geo
			} else {
				pending = append(pending, raw)
			}
		}
	}
	if len(pending) > model.TraceMaxHops*model.TraceMaxQueriesPerHop {
		return errors.New("路由地址数量超出限制")
	}
	var err error
	if len(pending) > 0 {
		if now.Before(c.cooldown) {
			err = errors.New("NextTrace API 冷却中")
		} else {
			lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err = c.lookup(lookupCtx, pending, found)
			cancel()
			if err != nil {
				if until := time.Now().Add(time.Minute); c.cooldown.Before(until) {
					c.cooldown = until
				}
			}
		}
	}
	for i := range trace.Hops {
		for _, address := range trace.Hops[i].Addresses {
			if geo, ok := found[address]; ok {
				geo.Source = SourceController
				trace.Hops[i].Geography = append(trace.Hops[i].Geography, geo)
			}
		}
	}
	if len(c.cache)+len(pending) > 4096 {
		c.cache = map[string]entry{}
	}
	for _, address := range pending {
		if geo, ok := found[address]; ok {
			c.cache[address] = entry{geo: geo, expires: now.Add(24 * time.Hour)}
		}
	}
	return err
}

func (c *Client) lookup(ctx context.Context, addresses []string, found map[string]model.NetworkTraceGeography) error {
	if c.v4Token != "" {
		for _, ip := range addresses {
			var raw json.RawMessage
			if err := c.request(ctx, http.MethodGet, "/v4/ipGeo?ip="+url.QueryEscape(ip), nil, &raw, true); err != nil {
				return err
			}
			geo, err := decodeGeography(raw, ip)
			if err != nil {
				return err
			}
			found[ip] = geo
		}
		return nil
	}
	if c.token == "" || time.Now().After(c.tokenExpires) {
		tokenCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		token, err := c.getToken(tokenCtx)
		cancel()
		if err != nil {
			return err
		}
		c.token = token
		c.tokenExpires = time.Now().Add(5 * time.Minute)
	}
	conn, resp, err := c.dialer.DialContext(ctx, c.wsEndpoint, http.Header{"User-Agent": []string{userAgent}, "Authorization": []string{"Bearer " + c.token}})
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		c.token = ""
		return errors.New("NextTrace API 连接失败")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	conn.SetReadLimit(64 * 1024)
	for _, ip := range addresses {
		deadline := time.Now().Add(2 * time.Second)
		if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
			deadline = end
		}
		if err = conn.SetWriteDeadline(deadline); err != nil {
			return errors.New("NextTrace API 写入失败")
		}
		if err = conn.WriteMessage(websocket.TextMessage, []byte(ip)); err != nil {
			return errors.New("NextTrace API 查询失败")
		}
		if err = conn.SetReadDeadline(deadline); err != nil {
			return errors.New("NextTrace API 读取失败")
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return errors.New("NextTrace API 响应超时或连接中断")
		}
		geo, err := decodeGeography(raw, ip)
		if err != nil {
			return err
		}
		found[ip] = geo
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("NextTrace API 查询超时")
		case <-timer.C:
		}
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, out any, v4 bool) error {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("NextTrace API 请求无效")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if v4 {
		req.Header.Set("X-NextTrace-Token", c.v4Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("NextTrace API 请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 429 {
			c.cooldown = time.Now().Add(15 * time.Minute)
			return errors.New("NextTrace API 已限流")
		}
		return errors.New("NextTrace API 返回 HTTP " + strconv.Itoa(resp.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return errors.New("NextTrace API 响应过大或读取失败")
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return errors.New("NextTrace API 响应无效")
	}
	return nil
}
func (c *Client) getToken(ctx context.Context) (string, error) {
	var challenge struct {
		Challenge struct {
			RequestID string `json:"request_id"`
			Number    string `json:"challenge"`
		} `json:"challenge"`
		RequestTime int64 `json:"request_time"`
	}
	if err := c.request(ctx, http.MethodGet, "/v3/challenge/request_challenge", nil, &challenge, false); err != nil {
		return "", err
	}
	if challenge.Challenge.RequestID == "" || len(challenge.Challenge.RequestID) > 256 {
		return "", errors.New("NextTrace API 挑战标识无效")
	}
	answer, err := solveChallenge(ctx, challenge.Challenge.Number)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(map[string]any{"challenge": map[string]string{"request_id": challenge.Challenge.RequestID}, "answer": answer, "request_time": challenge.RequestTime})
	var reply struct {
		Token string `json:"token"`
	}
	if err = c.request(ctx, http.MethodPost, "/v3/challenge/submit_answer", raw, &reply, false); err != nil {
		return "", err
	}
	if reply.Token == "" || len(reply.Token) > 8192 {
		return "", errors.New("NextTrace API 未返回有效令牌")
	}
	return reply.Token, nil
}

// The API challenge is a semiprime. Pollard rho runs within the caller budget
// and a strict input/iteration bound; untrusted responses cannot burn unlimited CPU.
func solveChallenge(ctx context.Context, raw string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, errors.New("NextTrace API 挑战计算已取消")
	}
	if len(raw) > 40 {
		return nil, errors.New("NextTrace API 挑战过大")
	}
	n, ok := new(big.Int).SetString(raw, 10)
	if !ok || len(raw) > 40 || n.Sign() <= 0 || n.BitLen() > 128 || n.ProbablyPrime(16) {
		return nil, errors.New("NextTrace API 挑战格式无效")
	}
	one := big.NewInt(1)
	factor := new(big.Int)
	if n.Bit(0) == 0 {
		factor.SetInt64(2)
	} else {
		for seed := int64(1); seed <= 16; seed++ {
			x, y := big.NewInt(2), big.NewInt(2)
			increment := big.NewInt(seed)
			delta := new(big.Int)
			step := func(v *big.Int) { v.Mul(v, v); v.Add(v, increment); v.Mod(v, n) }
			for i := 0; i < 200000; i++ {
				if i%128 == 0 && ctx.Err() != nil {
					return nil, errors.New("NextTrace API 挑战计算超时")
				}
				step(x)
				step(y)
				step(y)
				delta.Sub(x, y)
				delta.Abs(delta)
				factor.GCD(nil, nil, delta, n)
				if factor.Cmp(one) > 0 {
					break
				}
			}
			if factor.Cmp(one) > 0 && factor.Cmp(n) < 0 {
				break
			}
		}
	}
	if factor.Cmp(one) <= 0 || factor.Cmp(n) >= 0 {
		return nil, errors.New("NextTrace API 挑战超出计算预算")
	}
	other := new(big.Int).Div(n, factor)
	if !factor.ProbablyPrime(16) || !other.ProbablyPrime(16) {
		return nil, errors.New("NextTrace API 挑战不是半素数")
	}
	if factor.Cmp(other) > 0 {
		factor, other = other, factor
	}
	return []string{factor.String(), other.String()}, nil
}

func decodeGeography(raw []byte, address string) (model.NetworkTraceGeography, error) {
	var wire struct {
		IP       string          `json:"ip"`
		ASN      json.RawMessage `json:"asnumber"`
		Owner    string          `json:"owner"`
		Domain   string          `json:"domain"`
		ISP      string          `json:"isp"`
		Country  string          `json:"country"`
		Province string          `json:"prov"`
		City     string          `json:"city"`
		Prefix   string          `json:"prefix"`
		Router   json.RawMessage `json:"router"`
	}
	empty := model.NetworkTraceGeography{}
	if json.Unmarshal(raw, &wire) != nil || wire.IP != address {
		return empty, errors.New("NextTrace API 返回地址不匹配")
	}
	var asn string
	if json.Unmarshal(wire.ASN, &asn) != nil {
		asn = string(wire.ASN)
	}
	asn = strings.TrimPrefix(strings.TrimSpace(asn), "AS")
	number, err := strconv.ParseUint(asn, 10, 32)
	if err != nil || number == 0 {
		return empty, errors.New("NextTrace API 未返回有效 ASN")
	}
	for _, text := range []string{wire.Owner, wire.Domain, wire.ISP, wire.Country, wire.Province, wire.City, wire.Prefix} {
		if len(text) > 256 {
			return empty, errors.New("NextTrace API 归属字段过长")
		}
	}
	routers := map[string][]string{}
	if len(wire.Router) > 0 && string(wire.Router) != "null" {
		var encoded string
		if json.Unmarshal(wire.Router, &encoded) == nil {
			wire.Router = []byte(encoded)
		}
		if len(wire.Router) > 0 && json.Unmarshal(wire.Router, &routers) != nil {
			return empty, errors.New("NextTrace API 路由标注无效")
		}
	}
	if len(routers) > 16 {
		return empty, errors.New("NextTrace API 路由标注过大")
	}
	for key, values := range routers {
		if len(key) > 128 || len(values) > 16 {
			return empty, errors.New("NextTrace API 路由标注过大")
		}
		for _, v := range values {
			if len(v) > 256 {
				return empty, errors.New("NextTrace API 路由标注过大")
			}
		}
	}
	owner := wire.Domain
	if owner == "" {
		owner = wire.Owner
	}
	return model.NetworkTraceGeography{Address: address, ASN: "AS" + strconv.FormatUint(number, 10), Owner: owner, ISP: wire.ISP, Country: wire.Country, Province: wire.Province, City: wire.City, Prefix: wire.Prefix, Router: routers}, nil
}
