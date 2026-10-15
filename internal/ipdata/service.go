// Package ipdata separates IP attribution (ownership and location) from IP
// quality (proxy, VPN, Tor, hosting and abuse evidence). Each part queries
// several independent sources, keeps every source's raw verdict, and degrades
// automatically when a source is unconfigured, failing, rate limited or cooling
// down. Attribution treats IPinfo as authoritative and falls back to a vote of
// the remaining sources and finally the signed offline ip2region database.
package ipdata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
)

const (
	StatusOK             = "ok"
	StatusNotConfigured  = "not_configured"
	StatusUnavailable    = "unavailable"
	StatusQuotaExhausted = "quota_exhausted"
	StatusInvalidKey     = "invalid_key"
	StatusSkipped        = "skipped"
)

// Source is one provider's normalized answer. Unknown fields stay empty or
// nil; nothing is filled with a default.
type Source struct {
	Provider     string `json:"provider"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	CheckedAt    string `json:"checked_at"`
	Keyed        bool   `json:"keyed,omitempty"`
	ASN          string `json:"asn,omitempty"`
	ASName       string `json:"as_name,omitempty"`
	Organization string `json:"organization,omitempty"`
	CountryCode  string `json:"country_code,omitempty"`
	Country      string `json:"country,omitempty"`
	Region       string `json:"region,omitempty"`
	City         string `json:"city,omitempty"`
	Proxy        *bool  `json:"proxy,omitempty"`
	VPN          *bool  `json:"vpn,omitempty"`
	Tor          *bool  `json:"tor,omitempty"`
	Hosting      *bool  `json:"hosting,omitempty"`
	Abuser       *bool  `json:"abuser,omitempty"`
	Risk         *int   `json:"risk,omitempty"`
	Confidence   *int   `json:"confidence,omitempty"`
	AbuseScore   *int   `json:"abuse_score,omitempty"`
	Reports      *int   `json:"reports,omitempty"`
}

// Agreement counts how many sources that answered a field match the result.
type Agreement struct {
	Agree int `json:"agree"`
	Total int `json:"total"`
}

type Conflict struct {
	Provider string `json:"provider"`
	Field    string `json:"field"`
	Value    string `json:"value"`
}

// Attribution status: ok (IPinfo answered), degraded (other online sources),
// offline (only the local database), unknown (nothing answered).
type Attribution struct {
	Status       string            `json:"status"`
	Primary      string            `json:"primary,omitempty"`
	ASN          string            `json:"asn,omitempty"`
	ASName       string            `json:"as_name,omitempty"`
	CountryCode  string            `json:"country_code,omitempty"`
	Country      string            `json:"country,omitempty"`
	Region       string            `json:"region,omitempty"`
	City         string            `json:"city,omitempty"`
	FieldSources map[string]string `json:"field_sources"`
	ASNAgreement Agreement         `json:"asn_agreement"`
	CountryCheck Agreement         `json:"country_agreement"`
	Conflicts    []Conflict        `json:"conflicts"`
	Sources      []Source          `json:"sources"`
}

// Verdict is a tri-state vote: yes, no, conflict (equal votes) or unknown.
type Verdict struct {
	State    string   `json:"state"`
	Positive int      `json:"positive"`
	Negative int      `json:"negative"`
	Flagged  []string `json:"flagged_by"`
}

// Quality status: ok (every quality source answered), partial, unknown.
type Quality struct {
	Status      string   `json:"status"`
	Proxy       Verdict  `json:"proxy"`
	VPN         Verdict  `json:"vpn"`
	Tor         Verdict  `json:"tor"`
	Hosting     Verdict  `json:"hosting"`
	Abuser      Verdict  `json:"abuser"`
	Risk        *int     `json:"risk,omitempty"`
	RiskSource  string   `json:"risk_source,omitempty"`
	AbuseScore  *int     `json:"abuse_score,omitempty"`
	AbuseSource string   `json:"abuse_source,omitempty"`
	Reports     *int     `json:"reports,omitempty"`
	Sources     []Source `json:"sources"`
}

type Report struct {
	IP          string       `json:"ip"`
	CheckedAt   string       `json:"checked_at"`
	Attribution *Attribution `json:"attribution,omitempty"`
	Quality     *Quality     `json:"quality,omitempty"`
}

type Scope struct{ Attribution, Quality bool }

// Keys maps a provider name to its API key; empty means keyless or absent.
type Keys map[string]string

type Offline interface {
	Lookup(string) (model.IPGeography, error)
}

// ProviderHealth is the secret-free runtime state of one provider.
type ProviderHealth struct {
	Name          string `json:"name"`
	Configured    bool   `json:"configured"`
	UsedToday     int    `json:"used_today"`
	DailyBudget   int    `json:"daily_budget"`
	CooldownUntil string `json:"cooldown_until,omitempty"`
	LastStatus    string `json:"last_status,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastCheckedAt string `json:"last_checked_at,omitempty"`
}

type cacheEntry struct {
	source Source
	until  time.Time
}

type cooldown struct {
	until  time.Time
	status string
	err    string
}

type Service struct {
	HTTP     *http.Client
	offline  func() Offline
	mu       sync.Mutex
	keys     Keys
	cache    map[string]cacheEntry
	day      string
	used     map[string]int
	cooling  map[string]cooldown
	last     map[string]Source
	inflight map[string]*call
}

type call struct {
	done   chan struct{}
	source Source
}

func New(offline func() Offline) *Service {
	return &Service{
		HTTP:     &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 8 * time.Second, MaxResponseHeaderBytes: 16 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		offline:  offline,
		keys:     Keys{},
		cache:    map[string]cacheEntry{},
		used:     map[string]int{},
		cooling:  map[string]cooldown{},
		last:     map[string]Source{},
		inflight: map[string]*call{},
	}
}

func PublicIP(raw string) bool {
	ip, err := netip.ParseAddr(raw)
	return err == nil && pluginhttp.PublicIP(ip)
}

// SetKeys replaces the provider keys. Cache entries are keyed by key presence
// and every changed provider's cooldown is cleared so a fixed key takes effect
// on the next lookup.
func (s *Service) SetKeys(keys Keys) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := Keys{}
	for name, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			next[name] = key
		}
	}
	for _, item := range Catalog {
		if next[item.Name] != s.keys[item.Name] {
			delete(s.cooling, item.Name)
			for k := range s.cache {
				if strings.HasPrefix(k, item.Name+"|") {
					delete(s.cache, k)
				}
			}
		}
	}
	s.keys = next
}

func (s *Service) Key(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[name]
}

func (s *Service) Health() []ProviderHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollDay(time.Now())
	out := []ProviderHealth{}
	for _, item := range Catalog {
		h := ProviderHealth{Name: item.Name, Configured: s.keys[item.Name] != ""}
		if sp, ok := specs[item.Name]; ok {
			h.UsedToday, h.DailyBudget = s.used[item.Name], sp.budget(h.Configured)
		}
		if c, ok := s.cooling[item.Name]; ok && time.Now().Before(c.until) {
			h.CooldownUntil = c.until.UTC().Format(time.RFC3339)
		}
		if last, ok := s.last[item.Name]; ok {
			h.LastStatus, h.LastError, h.LastCheckedAt = last.Status, last.Error, last.CheckedAt
		}
		out = append(out, h)
	}
	return out
}

// Test bypasses cache and cooldown for one provider so an administrator can
// verify a key; it still counts against the local daily budget.
func (s *Service) Test(ctx context.Context, provider, ip string) Source {
	s.mu.Lock()
	delete(s.cooling, provider)
	for k := range s.cache {
		if strings.HasPrefix(k, provider+"|") && strings.HasSuffix(k, "|"+ip) {
			delete(s.cache, k)
		}
	}
	s.mu.Unlock()
	return s.fetch(ctx, provider, ip)
}

func (s *Service) Lookup(ctx context.Context, ip string, scope Scope) Report {
	report := Report{IP: ip, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if !PublicIP(ip) {
		if scope.Attribution {
			report.Attribution = &Attribution{Status: "unknown", FieldSources: map[string]string{}, Conflicts: []Conflict{}, Sources: []Source{}}
		}
		if scope.Quality {
			report.Quality = &Quality{Status: "unknown", Sources: []Source{}}
			fillVerdicts(report.Quality)
		}
		return report
	}
	names := []string{}
	if scope.Attribution {
		names = append(names, ProviderIPinfo, ProviderIPapiIs, ProviderIPWhois)
	}
	if scope.Quality {
		names = append(names, ProviderProxycheck, ProviderAbuseIPDB)
		if !scope.Attribution {
			names = append(names, ProviderIPapiIs)
		}
	}
	results := make([]Source, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.fetch(ctx, name, ip)
		}()
	}
	wg.Wait()
	byName := map[string]Source{}
	for _, r := range results {
		byName[r.Provider] = r
	}
	if scope.Attribution {
		sources := []Source{byName[ProviderIPinfo], byName[ProviderIPapiIs], byName[ProviderIPWhois]}
		if pc, ok := byName[ProviderProxycheck]; ok {
			sources = append(sources, pc)
		}
		sources = append(sources, s.offlineSource(ip))
		a := aggregateAttribution(sources)
		report.Attribution = &a
	}
	if scope.Quality {
		q := aggregateQuality([]Source{byName[ProviderProxycheck], byName[ProviderIPapiIs], byName[ProviderAbuseIPDB]})
		report.Quality = &q
	}
	return report
}

func (s *Service) offlineSource(ip string) Source {
	out := Source{Provider: ProviderOffline, Status: StatusUnavailable, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	var db Offline
	if s.offline != nil {
		db = s.offline()
	}
	if db == nil {
		out.Error = "离线数据库未加载"
		return out
	}
	geo, err := db.Lookup(ip)
	if err != nil || (geo.CountryCode == "" && geo.Country == "") {
		out.Error = "离线数据库无记录"
		return out
	}
	out.Status, out.CountryCode, out.Country, out.Region, out.City, out.Organization = StatusOK, geo.CountryCode, geo.Country, geo.Province, geo.City, geo.ISP
	normalizeAttribution(&out)
	return out
}

func (s *Service) rollDay(at time.Time) {
	if day := at.UTC().Format("2006-01-02"); s.day != day {
		s.day, s.used = day, map[string]int{}
	}
}

func (s *Service) fetch(ctx context.Context, provider, ip string) Source {
	at := time.Now()
	r := Source{Provider: provider, Status: StatusUnavailable, CheckedAt: at.UTC().Format(time.RFC3339)}
	sp, ok := specs[provider]
	if !ok {
		r.Error = "未知数据源"
		return r
	}
	s.mu.Lock()
	key := s.keys[provider]
	r.Keyed = key != ""
	cacheKey := provider + "|" + strconv.FormatBool(r.Keyed) + "|" + ip
	if cached, ok := s.cache[cacheKey]; ok && at.Before(cached.until) {
		s.mu.Unlock()
		return cached.source
	}
	if sp.keyRequired && key == "" {
		s.mu.Unlock()
		r.Status, r.Error = StatusNotConfigured, "未配置 API Key"
		return r
	}
	if c, ok := s.cooling[provider]; ok && at.Before(c.until) {
		s.mu.Unlock()
		r.Status, r.Error = c.status, c.err+"（暂停至 "+c.until.Local().Format("15:04")+"）"
		return r
	}
	if pending, ok := s.inflight[cacheKey]; ok {
		s.mu.Unlock()
		select {
		case <-pending.done:
			return pending.source
		case <-ctx.Done():
			r.Error = "查询已取消"
			return r
		}
	}
	s.rollDay(at)
	if s.used[provider] >= sp.budget(r.Keyed) {
		s.mu.Unlock()
		r.Status, r.Error = StatusQuotaExhausted, "今日本地查询预算已用完"
		return r
	}
	s.used[provider]++
	pending := &call{done: make(chan struct{})}
	s.inflight[cacheKey] = pending
	s.mu.Unlock()

	r = s.request(ctx, sp, provider, ip, key, r)

	s.mu.Lock()
	delete(s.inflight, cacheKey)
	s.last[provider] = r
	switch r.Status {
	case StatusOK:
		ttl := 6 * time.Hour
		if sp.attribution && !sp.quality {
			ttl = 24 * time.Hour
		}
		if len(s.cache) >= 8192 {
			s.cache = map[string]cacheEntry{}
		}
		s.cache[cacheKey] = cacheEntry{r, at.Add(ttl)}
		delete(s.cooling, provider)
	case StatusQuotaExhausted:
		s.cooling[provider] = cooldown{until: at.Add(15 * time.Minute), status: r.Status, err: r.Error}
	case StatusInvalidKey:
		s.cooling[provider] = cooldown{until: at.Add(30 * time.Minute), status: r.Status, err: r.Error}
	default:
		if ctx.Err() == nil {
			s.cooling[provider] = cooldown{until: at.Add(time.Minute), status: r.Status, err: r.Error}
		}
	}
	s.mu.Unlock()
	pending.source = r
	close(pending.done)
	return r
}

func (s *Service) request(ctx context.Context, sp spec, provider, ip, key string, r Source) Source {
	req, err := sp.request(ctx, ip, key)
	if err != nil {
		r.Error = "请求构造失败"
		return r
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OBoard-Controller")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		r.Error = "数据源请求失败"
		return r
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		r.Status, r.Error = StatusQuotaExhausted, "数据源已限流或额度用完"
		return r
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		if key == "" {
			r.Status, r.Error = StatusQuotaExhausted, "免 Key 接口拒绝访问，请填写 API Key"
		} else {
			r.Status, r.Error = StatusInvalidKey, "API Key 无效或无权限"
		}
		return r
	case resp.StatusCode != http.StatusOK:
		r.Error = "数据源返回 HTTP " + strconv.Itoa(resp.StatusCode)
		return r
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10+1))
	if err != nil || len(body) > 128<<10 {
		r.Error = "数据源响应无效"
		return r
	}
	if err = sp.decode(ip, body, key != "", &r); err != nil {
		if errors.Is(err, errQuota) {
			r.Status, r.Error = StatusQuotaExhausted, "数据源额度用完"
		} else {
			r.Error = "数据源响应缺少有效字段"
		}
		return r
	}
	r.Status = StatusOK
	return r
}

func aggregateAttribution(sources []Source) Attribution {
	a := Attribution{Status: "unknown", FieldSources: map[string]string{}, Conflicts: []Conflict{}, Sources: sources}
	ok := []Source{}
	for _, src := range sources {
		if src.Status == StatusOK {
			ok = append(ok, src)
		}
	}
	if len(ok) == 0 {
		return a
	}
	// IPinfo is authoritative whenever it answered; otherwise the remaining
	// online sources vote with priority order breaking ties.
	if ok[0].Provider == ProviderIPinfo {
		a.Status, a.Primary = "ok", ProviderIPinfo
		a.ASN, a.ASName, a.CountryCode, a.Country = ok[0].ASN, ok[0].ASName, ok[0].CountryCode, ok[0].Country
		for field, value := range map[string]string{"asn": a.ASN, "as_name": a.ASName, "country": a.CountryCode} {
			if value != "" {
				a.FieldSources[field] = ProviderIPinfo
			}
		}
	} else {
		online := []Source{}
		for _, src := range ok {
			if src.Provider != ProviderOffline {
				online = append(online, src)
			}
		}
		a.Status = "offline"
		if len(online) > 0 {
			a.Status = "degraded"
		}
		a.ASN = vote(online, func(s Source) string { return s.ASN })
		a.CountryCode = vote(ok, func(s Source) string { return s.CountryCode })
		for _, src := range online {
			if a.ASN != "" && src.ASN == a.ASN {
				a.Primary, a.FieldSources["asn"] = src.Provider, src.Provider
				break
			}
		}
		for _, src := range ok {
			if a.CountryCode != "" && src.CountryCode == a.CountryCode {
				a.FieldSources["country"] = src.Provider
				if a.Primary == "" {
					a.Primary = src.Provider
				}
				break
			}
		}
	}
	for _, src := range ok {
		if a.ASName == "" && a.ASN != "" && src.ASN == a.ASN && src.ASName != "" {
			a.ASName, a.FieldSources["as_name"] = src.ASName, src.Provider
		}
		if a.Country == "" && src.CountryCode == a.CountryCode && src.Country != "" && src.Provider != ProviderOffline {
			a.Country = src.Country
		}
	}
	// Region and city come from the first source that agrees on the country.
	for _, src := range ok {
		if src.CountryCode != a.CountryCode || src.City == "" && src.Region == "" {
			continue
		}
		a.Region, a.City = src.Region, src.City
		a.FieldSources["region"] = src.Provider
		break
	}
	if a.Country == "" {
		for _, src := range ok {
			if src.CountryCode == a.CountryCode && src.Country != "" {
				a.Country = src.Country
				break
			}
		}
	}
	for _, src := range ok {
		if src.ASN != "" && a.ASN != "" {
			a.ASNAgreement.Total++
			if src.ASN == a.ASN {
				a.ASNAgreement.Agree++
			} else {
				a.Conflicts = append(a.Conflicts, Conflict{Provider: src.Provider, Field: "asn", Value: src.ASN})
			}
		}
		if src.CountryCode != "" && a.CountryCode != "" {
			a.CountryCheck.Total++
			if src.CountryCode == a.CountryCode {
				a.CountryCheck.Agree++
			} else {
				a.Conflicts = append(a.Conflicts, Conflict{Provider: src.Provider, Field: "country", Value: src.CountryCode})
			}
		}
	}
	return a
}

func vote(sources []Source, field func(Source) string) string {
	counts := map[string]int{}
	best, bestCount := "", 0
	for _, src := range sources {
		if v := field(src); v != "" {
			counts[v]++
		}
	}
	for _, src := range sources {
		if v := field(src); v != "" && counts[v] > bestCount {
			best, bestCount = v, counts[v]
		}
	}
	return best
}

func aggregateQuality(sources []Source) Quality {
	q := Quality{Status: "unknown", Sources: sources}
	answered := 0
	for _, src := range sources {
		if src.Status != StatusOK {
			continue
		}
		answered++
		for _, item := range []struct {
			verdict *Verdict
			value   *bool
		}{{&q.Proxy, src.Proxy}, {&q.VPN, src.VPN}, {&q.Tor, src.Tor}, {&q.Hosting, src.Hosting}, {&q.Abuser, src.Abuser}} {
			if item.value == nil {
				continue
			}
			if *item.value {
				item.verdict.Positive++
				item.verdict.Flagged = append(item.verdict.Flagged, src.Provider)
			} else {
				item.verdict.Negative++
			}
		}
		if q.Risk == nil && src.Risk != nil {
			q.Risk, q.RiskSource = src.Risk, src.Provider
		}
		if q.AbuseScore == nil && src.AbuseScore != nil {
			q.AbuseScore, q.AbuseSource, q.Reports = src.AbuseScore, src.Provider, src.Reports
		}
	}
	switch {
	case answered == len(sources) && answered > 0:
		q.Status = "ok"
	case answered > 0:
		q.Status = "partial"
	}
	fillVerdicts(&q)
	return q
}

func fillVerdicts(q *Quality) {
	for _, v := range []*Verdict{&q.Proxy, &q.VPN, &q.Tor, &q.Hosting, &q.Abuser} {
		if v.Flagged == nil {
			v.Flagged = []string{}
		}
		switch {
		case v.Positive == 0 && v.Negative == 0:
			v.State = "unknown"
		case v.Positive > v.Negative:
			v.State = "yes"
		case v.Negative > v.Positive:
			v.State = "no"
		default:
			v.State = "conflict"
		}
	}
}

// Bool collapses a verdict to a definite answer; conflict and unknown are nil.
func (v Verdict) Bool() *bool {
	switch v.State {
	case "yes":
		t := true
		return &t
	case "no":
		f := false
		return &f
	}
	return nil
}
