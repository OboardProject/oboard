package ipdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	ProviderIPinfo     = "ipinfo"
	ProviderIPapiIs    = "ipapi_is"
	ProviderIPWhois    = "ipwhois"
	ProviderProxycheck = "proxycheck"
	ProviderAbuseIPDB  = "abuseipdb"
	ProviderOffline    = "ip2region"
	// ProviderNextTrace only holds the optional NextTrace v4 token; route
	// attribution itself lives in the nexttrace package.
	ProviderNextTrace = "nexttrace"
)

const (
	RoleAttribution = "attribution"
	RoleQuality     = "quality"
	RoleRoute       = "route"
)

// ProviderInfo is the public, secret-free description of one data source.
type ProviderInfo struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Roles       []string `json:"roles"`
	KeyRequired bool     `json:"key_required"`
	KeyLabel    string   `json:"key_label"`
	Keyless     string   `json:"keyless,omitempty"`
	SignupURL   string   `json:"signup_url"`
}

// Catalog lists every source that accepts a key, in priority order.
var Catalog = []ProviderInfo{
	{Name: ProviderIPinfo, Label: "IPinfo", Roles: []string{RoleAttribution}, KeyLabel: "Access Token", Keyless: "未填写时使用免 Key 接口，额度较低", SignupURL: "https://ipinfo.io/signup"},
	{Name: ProviderIPapiIs, Label: "ipapi.is", Roles: []string{RoleAttribution, RoleQuality}, KeyLabel: "API Key", Keyless: "未填写时每日额度很低，且不返回代理/VPN 检测", SignupURL: "https://ipapi.is/"},
	{Name: ProviderIPWhois, Label: "IPWhois", Roles: []string{RoleAttribution}, KeyLabel: "API Key", Keyless: "未填写时使用免费接口 ipwho.is", SignupURL: "https://ipwhois.io/"},
	{Name: ProviderProxycheck, Label: "Proxycheck", Roles: []string{RoleQuality, RoleAttribution}, KeyLabel: "API Key", Keyless: "未填写时每日约 100 次", SignupURL: "https://proxycheck.io/"},
	{Name: ProviderAbuseIPDB, Label: "AbuseIPDB", Roles: []string{RoleQuality}, KeyRequired: true, KeyLabel: "API Key", SignupURL: "https://www.abuseipdb.com/register"},
	{Name: ProviderNextTrace, Label: "NextTrace", Roles: []string{RoleRoute}, KeyLabel: "API Token", Keyless: "未填写时使用官方免费接口（v3）", SignupURL: "https://www.nxtrace.org/"},
}

func KnownKeyProvider(name string) bool {
	for _, item := range Catalog {
		if item.Name == name {
			return true
		}
	}
	return false
}

var asnPattern = regexp.MustCompile(`^AS[1-9][0-9]{0,9}$`)
var countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

type spec struct {
	attribution, quality bool
	keyRequired          bool
	budget               func(keyed bool) int
	request              func(ctx context.Context, ip, key string) (*http.Request, error)
	decode               func(ip string, body []byte, keyed bool, out *Source) error
}

var errQuota = errors.New("quota")

var specs = map[string]spec{
	ProviderIPinfo: {attribution: true, budget: func(keyed bool) int { return pick(keyed, 100000, 800) },
		request: func(ctx context.Context, ip, key string) (*http.Request, error) {
			if key == "" {
				return http.NewRequestWithContext(ctx, http.MethodGet, "https://ipinfo.io/"+ip+"/json", nil)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipinfo.io/lite/"+ip, nil)
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			return req, err
		},
		decode: func(ip string, body []byte, keyed bool, out *Source) error {
			var v struct {
				IP          string `json:"ip"`
				ASN         string `json:"asn"`
				ASName      string `json:"as_name"`
				CountryCode string `json:"country_code"`
				Country     string `json:"country"`
				Region      string `json:"region"`
				City        string `json:"city"`
				Org         string `json:"org"`
				Bogon       bool   `json:"bogon"`
			}
			if err := json.Unmarshal(body, &v); err != nil || v.Bogon {
				return errors.New("invalid")
			}
			if keyed {
				out.ASN, out.ASName, out.CountryCode, out.Country = v.ASN, v.ASName, v.CountryCode, v.Country
			} else {
				// The keyless endpoint returns "AS15169 Google LLC" and a bare country code.
				asn, name, _ := strings.Cut(strings.TrimSpace(v.Org), " ")
				out.ASN, out.ASName, out.CountryCode, out.Region, out.City = asn, name, v.Country, v.Region, v.City
			}
			if v.IP != "" && v.IP != ip {
				return errors.New("address mismatch")
			}
			return requireAttribution(out)
		}},
	ProviderIPapiIs: {attribution: true, quality: true, budget: func(keyed bool) int { return pick(keyed, 950, 25) },
		request: func(ctx context.Context, ip, key string) (*http.Request, error) {
			payload := map[string]string{"q": ip}
			if key != "" {
				payload["key"] = key
			}
			raw, _ := json.Marshal(payload)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.ipapi.is", bytes.NewReader(raw))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
			}
			return req, err
		},
		decode: decodeIPapiIs},
	ProviderIPWhois: {attribution: true, budget: func(keyed bool) int { return pick(keyed, 100000, 300) },
		request: func(ctx context.Context, ip, key string) (*http.Request, error) {
			if key == "" {
				return http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwho.is/"+ip, nil)
			}
			return http.NewRequestWithContext(ctx, http.MethodGet, "https://ipwhois.pro/"+ip+"?key="+url.QueryEscape(key), nil)
		},
		decode: func(ip string, body []byte, _ bool, out *Source) error {
			var v struct {
				IP          string `json:"ip"`
				Success     *bool  `json:"success"`
				Message     string `json:"message"`
				CountryCode string `json:"country_code"`
				Country     string `json:"country"`
				Region      string `json:"region"`
				City        string `json:"city"`
				Connection  struct {
					ASN int64  `json:"asn"`
					Org string `json:"org"`
					ISP string `json:"isp"`
				} `json:"connection"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return errors.New("invalid")
			}
			if v.Success != nil && !*v.Success {
				if strings.Contains(strings.ToLower(v.Message), "limit") {
					return errQuota
				}
				return errors.New("provider error")
			}
			if v.IP != "" && v.IP != ip {
				return errors.New("address mismatch")
			}
			if v.Connection.ASN > 0 {
				out.ASN = "AS" + strconv.FormatInt(v.Connection.ASN, 10)
			}
			out.ASName, out.Organization = v.Connection.Org, v.Connection.ISP
			out.CountryCode, out.Country, out.Region, out.City = v.CountryCode, v.Country, v.Region, v.City
			return requireAttribution(out)
		}},
	ProviderProxycheck: {quality: true, attribution: true, budget: func(keyed bool) int { return pick(keyed, 900, 90) },
		request: func(ctx context.Context, ip, key string) (*http.Request, error) {
			endpoint := "https://proxycheck.io/v3/" + ip + "?tag=oboard"
			if key != "" {
				endpoint += "&key=" + url.QueryEscape(key)
			}
			return http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		},
		decode: func(ip string, body []byte, _ bool, out *Source) error {
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(body, &envelope); err != nil {
				return err
			}
			var status, message string
			_ = json.Unmarshal(envelope["status"], &status)
			_ = json.Unmarshal(envelope["message"], &message)
			if status == "denied" && strings.Contains(strings.ToLower(message), "limit") {
				return errQuota
			}
			if status != "ok" && status != "warning" {
				return errors.New("provider failure")
			}
			var v struct {
				Network struct {
					ASN      string `json:"asn"`
					Provider string `json:"provider"`
					Org      string `json:"organisation"`
				} `json:"network"`
				Location struct {
					CountryCode string `json:"country_code"`
					Country     string `json:"country_name"`
					Region      string `json:"region_name"`
					City        string `json:"city_name"`
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
			out.ASN, out.ASName, out.Organization = v.Network.ASN, v.Network.Provider, v.Network.Org
			out.CountryCode, out.Country, out.Region, out.City = v.Location.CountryCode, v.Location.Country, v.Location.Region, v.Location.City
			out.Proxy, out.VPN, out.Tor, out.Hosting = v.Detections.Proxy, v.Detections.VPN, v.Detections.Tor, v.Detections.Hosting
			out.Risk, out.Confidence = boundedScore(v.Detections.Risk), boundedScore(v.Detections.Confidence)
			normalizeAttribution(out)
			return nil
		}},
	ProviderAbuseIPDB: {quality: true, keyRequired: true, budget: func(bool) int { return 900 },
		request: func(ctx context.Context, ip, key string) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.abuseipdb.com/api/v2/check?ipAddress="+url.QueryEscape(ip)+"&maxAgeInDays=30", nil)
			if err == nil {
				req.Header.Set("Key", key)
			}
			return req, err
		},
		decode: func(ip string, body []byte, _ bool, out *Source) error {
			var v struct {
				Data struct {
					IP        string `json:"ipAddress"`
					Score     *int   `json:"abuseConfidenceScore"`
					Reports   *int   `json:"totalReports"`
					ISP       string `json:"isp"`
					Country   string `json:"countryCode"`
					UsageType string `json:"usageType"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &v); err != nil {
				return err
			}
			if v.Data.Score == nil {
				return errors.New("missing score")
			}
			if v.Data.IP != "" && v.Data.IP != ip {
				return errors.New("address mismatch")
			}
			out.AbuseScore, out.Reports, out.Organization, out.CountryCode = boundedScore(v.Data.Score), v.Data.Reports, v.Data.ISP, v.Data.Country
			abuser := *out.AbuseScore >= 50
			out.Abuser = &abuser
			usage := strings.ToLower(v.Data.UsageType)
			switch {
			case strings.Contains(usage, "data center"), strings.Contains(usage, "hosting"):
				hosting := true
				out.Hosting = &hosting
			case strings.Contains(usage, "isp"):
				hosting := false
				out.Hosting = &hosting
			}
			normalizeAttribution(out)
			return nil
		}},
}

// flexBool accepts a detection flag that ipapi.is documents as a boolean or
// any other truthy value. A strict *bool rejects the whole document.
type flexBool struct {
	set bool
	v   bool
}

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	switch raw[0] {
	case 't', 'T':
		b.set, b.v = true, true
	case 'f', 'F':
		b.set, b.v = true, false
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		b.set = true
		b.v = text != "" && !strings.EqualFold(text, "false") && text != "0"
	case '{', '[':
		b.set, b.v = true, true
	default:
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		b.set, b.v = true, n != 0
	}
	return nil
}

func (b flexBool) ptr() *bool {
	if !b.set {
		return nil
	}
	v := b.v
	return &v
}

func decodeIPapiIs(ip string, body []byte, _ bool, out *Source) error {
	var v struct {
		IP         string          `json:"ip"`
		Error      string          `json:"error"`
		Code       string          `json:"error_code"`
		Bogon      bool            `json:"is_bogon"`
		Proxy      flexBool        `json:"is_proxy"`
		VPN        flexBool        `json:"is_vpn"`
		Tor        flexBool        `json:"is_tor"`
		Datacenter flexBool        `json:"is_datacenter"`
		Abuser     flexBool        `json:"is_abuser"`
		Company    json.RawMessage `json:"company"`
		ASN        json.RawMessage `json:"asn"`
		Location   json.RawMessage `json:"location"`
		City       string          `json:"city"`
		Region     string          `json:"region"`
		Country    string          `json:"country"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Bogon {
		return errors.New("invalid")
	}
	if v.Error != "" || v.Code != "" {
		if ipapiQuota(v.Code, v.Error) {
			return errQuota
		}
		return errors.New("provider error")
	}
	if v.IP != "" && v.IP != ip {
		return errors.New("address mismatch")
	}
	if err := assignIPapiCompany(v.Company, out); err != nil {
		return err
	}
	if err := assignIPapiASN(v.ASN, out); err != nil {
		return err
	}
	if len(bytes.TrimSpace(v.Location)) > 0 && string(bytes.TrimSpace(v.Location)) != "null" {
		var loc struct {
			CountryCode string `json:"country_code"`
			Country     string `json:"country"`
			State       string `json:"state"`
			City        string `json:"city"`
		}
		if err := json.Unmarshal(v.Location, &loc); err != nil {
			return errors.New("invalid")
		}
		out.CountryCode, out.Country, out.Region, out.City = loc.CountryCode, loc.Country, loc.State, loc.City
	} else {
		out.Country, out.Region, out.City = v.Country, v.Region, v.City
	}
	out.Proxy, out.VPN, out.Tor, out.Hosting, out.Abuser = v.Proxy.ptr(), v.VPN.ptr(), v.Tor.ptr(), v.Datacenter.ptr(), v.Abuser.ptr()
	normalizeAttribution(out)
	if out.ASN == "" && out.CountryCode == "" && out.Country == "" && out.Region == "" && out.City == "" &&
		out.Proxy == nil && out.VPN == nil && out.Tor == nil && out.Hosting == nil && out.Abuser == nil {
		return errors.New("missing attribution")
	}
	return nil
}

func ipapiQuota(code, message string) bool {
	switch strings.ToUpper(code) {
	case "ERR_QUOTA_EXCEEDED", "ERR_FREE_TIER_EXHAUSTED":
		return true
	}
	msg := strings.ToLower(message)
	return strings.Contains(msg, "limit") || strings.Contains(msg, "quota") || strings.Contains(msg, "exhausted")
}

func assignIPapiCompany(raw json.RawMessage, out *Source) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	switch raw[0] {
	case '"':
		return json.Unmarshal(raw, &out.Organization)
	case '{':
		var company struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &company); err != nil {
			return errors.New("invalid")
		}
		out.Organization = company.Name
	case '[':
		var companies []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &companies); err != nil {
			return errors.New("invalid")
		}
		if len(companies) > 0 {
			out.Organization = companies[0].Name
		}
	}
	return nil
}

func assignIPapiASN(raw json.RawMessage, out *Source) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	switch raw[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return errors.New("invalid")
		}
		asn, name, _ := strings.Cut(strings.TrimSpace(text), " ")
		out.ASN, out.ASName = asn, name
	case '{':
		var asn struct {
			ASN int64  `json:"asn"`
			Org string `json:"org"`
		}
		if err := json.Unmarshal(raw, &asn); err != nil {
			return errors.New("invalid")
		}
		if asn.ASN > 0 {
			out.ASN = "AS" + strconv.FormatInt(asn.ASN, 10)
		}
		out.ASName = asn.Org
	default:
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return errors.New("invalid")
		}
		if n > 0 {
			out.ASN = "AS" + strconv.FormatInt(n, 10)
		}
	}
	return nil
}

func pick(keyed bool, withKey, without int) int {
	if keyed {
		return withKey
	}
	return without
}

func boundedScore(v *int) *int {
	if v == nil {
		return nil
	}
	n := *v
	if n < 0 {
		n = 0
	}
	if n > 100 {
		n = 100
	}
	return &n
}

func normalizeAttribution(out *Source) {
	out.ASN = strings.ToUpper(strings.TrimSpace(out.ASN))
	if out.ASN != "" && !strings.HasPrefix(out.ASN, "AS") {
		out.ASN = "AS" + out.ASN
	}
	if !asnPattern.MatchString(out.ASN) {
		out.ASN = ""
	}
	out.CountryCode = strings.ToUpper(strings.TrimSpace(out.CountryCode))
	if !countryPattern.MatchString(out.CountryCode) {
		out.CountryCode = ""
	}
	for _, field := range []*string{&out.ASName, &out.Organization, &out.Country, &out.Region, &out.City} {
		*field = strings.TrimSpace(*field)
		if runes := []rune(*field); len(runes) > 120 {
			*field = string(runes[:120])
		}
	}
}

func requireAttribution(out *Source) error {
	normalizeAttribution(out)
	if out.ASN == "" && out.CountryCode == "" {
		return errors.New("missing attribution")
	}
	return nil
}
