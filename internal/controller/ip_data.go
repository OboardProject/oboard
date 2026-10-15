package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/ipdata"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/nexttrace"
	"github.com/OboardProject/oboard/internal/security"
)

// ipDataKeysSetting holds every IP data provider key as one JSON object
// encrypted with OBOARD_SESSION_SECRET. Keys are never returned by any API.
const ipDataKeysSetting = "ip_data_provider_keys"

type ipDataRuntime struct {
	once      sync.Once
	mu        sync.Mutex
	service   *ipdata.Service
	nextTrace routeFiller
	loaded    bool
}

// routeFiller is the Controller NextTrace client; tests substitute it.
type routeFiller interface {
	Fill(context.Context, *model.NetworkTraceResult) error
	SetToken(string)
}

func (s *Server) ipDataState() *ipDataRuntime {
	s.ipData.once.Do(func() {
		s.ipData.service = ipdata.New(func() ipdata.Offline {
			if s.geoIP == nil {
				return nil
			}
			return s.geoIP
		})
		s.ipData.nextTrace = nexttrace.New(time.Now, "")
	})
	return &s.ipData
}

// ipDataService returns the shared provider service with the stored keys
// loaded once; later key changes are pushed by the settings handler.
func (s *Server) ipDataService(ctx context.Context) *ipdata.Service {
	state := s.ipDataState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.loaded {
		keys, err := s.loadIPDataKeys(ctx)
		if err != nil {
			log.Printf("load ip data provider keys: %v", err)
		} else {
			state.loaded = true
		}
		state.service.SetKeys(keys)
		state.nextTrace.SetToken(keys[ipdata.ProviderNextTrace])
	}
	return state.service
}

func (s *Server) loadIPDataKeys(ctx context.Context) (ipdata.Keys, error) {
	keys := ipdata.Keys{}
	raw, err := s.store.GetSetting(ctx, ipDataKeysSetting)
	if err != nil || strings.TrimSpace(raw) == "" {
		return keys, nil
	}
	plain, err := security.DecryptSecret(s.sessionSecret, ipDataKeysSetting, raw)
	if err != nil {
		return keys, errors.New("IP 数据源密钥无法解密，请重新填写")
	}
	if err = json.Unmarshal([]byte(plain), &keys); err != nil {
		return ipdata.Keys{}, errors.New("IP 数据源密钥已损坏，请重新填写")
	}
	for name := range keys {
		if !ipdata.KnownKeyProvider(name) {
			delete(keys, name)
		}
	}
	return keys, nil
}

func (s *Server) ipDataProviders(ctx context.Context) map[string]any {
	service := s.ipDataService(ctx)
	health := map[string]ipdata.ProviderHealth{}
	for _, h := range service.Health() {
		health[h.Name] = h
	}
	rows := []map[string]any{}
	for _, info := range ipdata.Catalog {
		h := health[info.Name]
		rows = append(rows, map[string]any{"name": info.Name, "label": info.Label, "roles": info.Roles, "key_required": info.KeyRequired, "key_label": info.KeyLabel, "keyless": info.Keyless, "signup_url": info.SignupURL, "configured": h.Configured, "used_today": h.UsedToday, "daily_budget": h.DailyBudget, "cooldown_until": h.CooldownUntil, "last_status": h.LastStatus, "last_error": h.LastError, "last_checked_at": h.LastCheckedAt})
	}
	return map[string]any{"providers": rows, "offline": map[string]any{"available": s.geoIP != nil, "provider": ipdata.ProviderOffline, "error": s.geoIPStatus.Error}}
}

type ipDataKeysRequest struct {
	Keys  map[string]string `json:"keys"`
	Clear []string          `json:"clear"`
}

// ipDataKeys reads provider status and saves keys. It is an interactive
// administrator surface only: keys never travel through MCP or automation.
func (s *Server) ipDataKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		write(w, 200, s.ipDataProviders(r.Context()))
	case http.MethodPost:
		var req ipDataKeysRequest
		if !decode(w, r, &req) {
			return
		}
		keys, err := s.loadIPDataKeys(r.Context())
		if err != nil {
			keys = ipdata.Keys{}
		}
		changed := []string{}
		for name, value := range req.Keys {
			value = strings.TrimSpace(value)
			if !ipdata.KnownKeyProvider(name) {
				fail(w, errors.New("未知数据源："+name), 400)
				return
			}
			if value == "" {
				continue
			}
			if len(value) > 512 || strings.ContainsAny(value, " \t\r\n") {
				fail(w, errors.New("API Key 格式无效"), 400)
				return
			}
			keys[name] = value
			changed = append(changed, name)
		}
		for _, name := range req.Clear {
			if !ipdata.KnownKeyProvider(name) {
				fail(w, errors.New("未知数据源："+name), 400)
				return
			}
			delete(keys, name)
			changed = append(changed, name)
		}
		raw, _ := json.Marshal(keys)
		wrapped, err := security.EncryptSecret(s.sessionSecret, ipDataKeysSetting, string(raw))
		if err != nil {
			fail(w, err, 500)
			return
		}
		if err = s.store.SetSetting(r.Context(), ipDataKeysSetting, wrapped); err != nil {
			fail(w, err, 500)
			return
		}
		state := s.ipDataState()
		state.mu.Lock()
		state.service.SetKeys(keys)
		state.nextTrace.SetToken(keys[ipdata.ProviderNextTrace])
		state.loaded = true
		state.mu.Unlock()
		auditReq(s, r, "update", "ip-data-keys", strings.Join(changed, ","))
		write(w, 200, s.ipDataProviders(r.Context()))
	default:
		method(w)
	}
}

// ipDataTest checks one provider with the saved key against a fixed public
// address, bypassing cache and cooldown.
func (s *Server) ipDataTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	service := s.ipDataService(ctx)
	if req.Provider == ipdata.ProviderNextTrace {
		trace := model.NetworkTraceResult{Hops: []model.NetworkTraceHop{{Hop: 1, Addresses: []string{"1.1.1.1"}}}}
		state := s.ipDataState()
		if err := state.nextTrace.Fill(ctx, &trace); err != nil {
			write(w, 200, map[string]any{"result": ipdata.Source{Provider: req.Provider, Status: ipdata.StatusUnavailable, Error: err.Error(), CheckedAt: time.Now().UTC().Format(time.RFC3339)}})
			return
		}
		result := ipdata.Source{Provider: req.Provider, Status: ipdata.StatusOK, CheckedAt: time.Now().UTC().Format(time.RFC3339), Keyed: service.Key(ipdata.ProviderNextTrace) != ""}
		if geo := trace.Hops[0].Geography; len(geo) > 0 {
			result.ASN, result.ASName, result.Country = geo[0].ASN, geo[0].Owner, geo[0].Country
		}
		write(w, 200, map[string]any{"result": result})
		return
	}
	if !ipdata.KnownKeyProvider(req.Provider) {
		fail(w, errors.New("未知数据源"), 400)
		return
	}
	write(w, 200, map[string]any{"result": service.Test(ctx, req.Provider, "1.1.1.1")})
}

type ipDataLookupInput struct {
	IP          string `json:"ip"`
	Attribution *bool  `json:"attribution,omitempty"`
	Quality     *bool  `json:"quality,omitempty"`
}

func (s *Server) queryIPData(ctx context.Context, _ application.Principal, name string, raw json.RawMessage) (any, error) {
	if name == "ip_data.providers.list" {
		if err := strictAutomationInput(raw, &struct{}{}); err != nil {
			return nil, err
		}
		return s.ipDataProviders(ctx), nil
	}
	var input ipDataLookupInput
	if err := strictAutomationInput(raw, &input); err != nil {
		return nil, err
	}
	input.IP = strings.TrimSpace(input.IP)
	if !ipdata.PublicIP(input.IP) {
		return nil, errors.New("只允许查询公网 IP")
	}
	scope := ipdata.Scope{Attribution: input.Attribution == nil || *input.Attribution, Quality: input.Quality == nil || *input.Quality}
	if !scope.Attribution && !scope.Quality {
		return nil, errors.New("请至少选择归属或质量")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return s.ipDataService(lookupCtx).Lookup(lookupCtx, input.IP, scope), nil
}

func (s *Server) apiIPData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, 405, "method_not_allowed", "不支持该方法")
		return
	}
	p, _ := apiPrincipal(r)
	name, raw := "ip_data.providers.list", json.RawMessage(`{}`)
	if r.URL.Path == "/api/v1/ip-data/lookup" {
		name = "ip_data.lookup"
		raw, _ = json.Marshal(ipDataLookupInput{IP: r.URL.Query().Get("ip")})
	}
	if _, ok := s.capabilities.Authorize(p, name); !ok {
		v2Error(w, r, 403, "capability_denied", "没有读取权限")
		return
	}
	value, err := s.queryIPData(r.Context(), p, name, raw)
	if err != nil {
		v2Error(w, r, 400, "invalid_input", err.Error())
		return
	}
	v2Write(w, r, 200, value, nil)
}

// completeTraceGeography keeps the Agent's NextTrace attribution as primary,
// asks the Controller NextTrace client for hops the Agent could not
// attribute, and finally falls back to the IP attribution chain for the ASN.
func (s *Server) completeTraceGeography(ctx context.Context, trace *model.NetworkTraceResult) {
	if trace.Provider == "" {
		trace.Provider = nexttrace.Provider
	}
	missing := func() []string {
		out := []string{}
		for _, hop := range trace.Hops {
			have := map[string]bool{}
			for _, geo := range hop.Geography {
				if geo.ASN != "" {
					have[geo.Address] = true
				}
			}
			for _, address := range hop.Addresses {
				if ipdata.PublicIP(address) && !have[address] {
					out = append(out, address)
				}
			}
		}
		return out
	}
	if len(missing()) == 0 {
		trace.GeographyError = ""
		return
	}
	service := s.ipDataService(ctx)
	state := s.ipDataState()
	nextTraceErr := state.nextTrace.Fill(ctx, trace)
	pending := missing()
	if len(pending) == 0 {
		trace.GeographyError = ""
		return
	}
	for i := range trace.Hops {
		for _, address := range trace.Hops[i].Addresses {
			if !stringSliceHas(pending, address) {
				continue
			}
			a := service.Lookup(ctx, address, ipdata.Scope{Attribution: true}).Attribution
			if a == nil || a.ASN == "" {
				continue
			}
			trace.Hops[i].Geography = append(trace.Hops[i].Geography, model.NetworkTraceGeography{Address: address, ASN: a.ASN, Owner: a.ASName, Country: a.Country, Province: a.Region, City: a.City, Source: a.Primary})
		}
	}
	if left := missing(); len(left) > 0 {
		message := "部分路由跳归属查询失败"
		if nextTraceErr != nil {
			message = nextTraceErr.Error() + "，备用数据源也未能补全"
		}
		trace.GeographyError = message
		return
	}
	trace.GeographyError = ""
}
