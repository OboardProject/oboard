package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- Alibaba Cloud DNS API v1 requires HMAC-SHA1 signatures.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type dnsProviderClient interface {
	Verify(context.Context) error
	ListRecords(context.Context) ([]model.DNSRecord, error)
	UpsertRecord(context.Context, model.DNSRecord) (model.DNSRecord, error)
	DeleteRecord(context.Context, string) error
}

type dnsProviderEndpoints struct {
	cloudflare string
	aliDNS     string
	tencentDNS string
	tencentESA string
	huaweiIAM  string
	huaweiDNS  string
}

func defaultDNSProviderEndpoints() dnsProviderEndpoints {
	return dnsProviderEndpoints{
		cloudflare: defaultCloudflareAPIBase,
		aliDNS:     "https://alidns.aliyuncs.com/",
		tencentDNS: "https://dnspod.tencentcloudapi.com",
		tencentESA: "https://teo.tencentcloudapi.com",
		huaweiIAM:  "https://iam.myhuaweicloud.com/v3/auth/tokens",
	}
}

func (s *Server) dnsProviderClient(credential model.DNSCredential) (dnsProviderClient, error) {
	plaintext, err := security.DecryptSecret(s.sessionSecret, "dns-credential", credential.ConfigEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt DNS credential: %w", err)
	}
	config := map[string]string{}
	if err := json.Unmarshal([]byte(plaintext), &config); err != nil {
		return nil, errors.New("invalid encrypted DNS credential")
	}
	if err := validateDNSProviderConfig(credential.Provider, config); err != nil {
		return nil, err
	}
	base := dnsProviderBase{credential: credential, httpClient: &http.Client{Timeout: 20 * time.Second}}
	switch credential.Provider {
	case model.DNSProviderCloudflare:
		apiBase := s.dnsEndpoints.cloudflare
		if apiBase == "" {
			apiBase = defaultCloudflareAPIBase
		}
		return &cloudflareDNSProvider{dnsProviderBase: base, token: config["api_token"], accountID: config["account_id"], apiBase: apiBase}, nil
	case model.DNSProviderAliDNS:
		return &aliDNSProvider{dnsProviderBase: base, accessKeyID: config["access_key_id"], accessKeySecret: config["access_key_secret"], apiBase: s.dnsEndpoints.aliDNS}, nil
	case model.DNSProviderTencentDNS:
		return &tencentDNSProvider{dnsProviderBase: base, secretID: config["secret_id"], secretKey: config["secret_key"], endpoint: s.dnsEndpoints.tencentDNS}, nil
	case model.DNSProviderTencentESA:
		return &tencentESAProvider{dnsProviderBase: base, secretID: config["secret_id"], secretKey: config["secret_key"], endpoint: s.dnsEndpoints.tencentESA}, nil
	case model.DNSProviderHuaweiCloud:
		return &huaweiDNSProvider{dnsProviderBase: base, username: config["username"], password: config["password"], domainName: config["domain_name"], region: config["region"], iamEndpoint: s.dnsEndpoints.huaweiIAM, dnsEndpoint: s.dnsEndpoints.huaweiDNS}, nil
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", credential.Provider)
	}
}

func validateDNSProviderConfig(provider model.DNSProvider, config map[string]string) error {
	required := map[model.DNSProvider][]string{
		model.DNSProviderCloudflare:  {"api_token"},
		model.DNSProviderAliDNS:      {"access_key_id", "access_key_secret"},
		model.DNSProviderTencentDNS:  {"secret_id", "secret_key"},
		model.DNSProviderTencentESA:  {"secret_id", "secret_key"},
		model.DNSProviderHuaweiCloud: {"username", "password", "domain_name", "region"},
	}[provider]
	if len(required) == 0 {
		return fmt.Errorf("unsupported DNS provider %q", provider)
	}
	for _, key := range required {
		if strings.TrimSpace(config[key]) == "" {
			return fmt.Errorf("DNS provider %s requires %s", provider, key)
		}
	}
	return nil
}

type dnsProviderBase struct {
	credential model.DNSCredential
	httpClient *http.Client
}

func (b dnsProviderBase) record(id, recordType, name, content string, ttl int, proxied, enabled bool) model.DNSRecord {
	return model.DNSRecord{ID: id, CredentialID: b.credential.ID, ZoneID: b.credential.ZoneID, ZoneName: b.credential.ZoneName, Type: strings.ToUpper(recordType), Name: normalizeDomainName(name), Content: strings.TrimSpace(content), TTL: ttl, Proxied: proxied, Enabled: enabled}
}

type cloudflareDNSProvider struct {
	dnsProviderBase
	token     string
	accountID string
	apiBase   string
}

func (p *cloudflareDNSProvider) client() *cloudflareClient {
	client := newCloudflareClient(p.token, p.apiBase)
	client.httpClient = p.httpClient
	return client
}

func (p *cloudflareDNSProvider) zone(ctx context.Context) (cloudflareZone, error) {
	client := p.client()
	if strings.TrimSpace(p.credential.ZoneID) != "" {
		var zone cloudflareZone
		if err := client.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(p.credential.ZoneID), nil, nil, &zone); err != nil {
			return cloudflareZone{}, err
		}
		return zone, nil
	}
	return client.findZone(ctx, p.credential.ZoneName)
}

func (p *cloudflareDNSProvider) Verify(ctx context.Context) error {
	if _, err := p.client().verifyToken(ctx); err != nil {
		return err
	}
	_, err := p.zone(ctx)
	return err
}

func (p *cloudflareDNSProvider) ListRecords(ctx context.Context) ([]model.DNSRecord, error) {
	zone, err := p.zone(ctx)
	if err != nil {
		return nil, err
	}
	items, err := p.client().listDNSRecords(ctx, zone)
	if err != nil {
		return nil, err
	}
	out := make([]model.DNSRecord, 0, len(items))
	for _, item := range items {
		record := p.record(item.ID, item.Type, item.Name, item.Content, item.TTL, item.Proxied, true)
		record.Comment = item.Comment
		out = append(out, record)
	}
	return out, nil
}

func (p *cloudflareDNSProvider) UpsertRecord(ctx context.Context, record model.DNSRecord) (model.DNSRecord, error) {
	zone, err := p.zone(ctx)
	if err != nil {
		return model.DNSRecord{}, err
	}
	payload := map[string]any{"type": strings.ToUpper(record.Type), "name": normalizeDomainName(record.Name), "content": strings.TrimSpace(record.Content), "comment": record.Comment, "ttl": record.TTL, "proxied": record.Proxied}
	if record.TTL <= 0 {
		payload["ttl"] = 1
	}
	method := http.MethodPost
	path := "/zones/" + url.PathEscape(zone.ID) + "/dns_records"
	if record.ID != "" {
		method = http.MethodPatch
		path += "/" + url.PathEscape(record.ID)
	}
	var saved cloudflareDNSRecord
	if err := p.client().do(ctx, method, path, nil, payload, &saved); err != nil {
		return model.DNSRecord{}, err
	}
	out := p.record(saved.ID, saved.Type, saved.Name, saved.Content, saved.TTL, saved.Proxied, true)
	out.Comment = saved.Comment
	if out.Comment == "" {
		out.Comment = record.Comment
	}
	return out, nil
}

func (p *cloudflareDNSProvider) DeleteRecord(ctx context.Context, id string) error {
	zone, err := p.zone(ctx)
	if err != nil {
		return err
	}
	return p.client().deleteDNSRecord(ctx, zone.ID, id)
}

type aliDNSProvider struct {
	dnsProviderBase
	accessKeyID     string
	accessKeySecret string
	apiBase         string
}

type aliDNSResponse struct {
	Code          string `json:"Code"`
	Message       string `json:"Message"`
	RecordID      string `json:"RecordId"`
	DomainRecords struct {
		Records []struct {
			ID     string `json:"RecordId"`
			RR     string `json:"RR"`
			Type   string `json:"Type"`
			Value  string `json:"Value"`
			TTL    int    `json:"TTL"`
			Status string `json:"Status"`
			Remark string `json:"Remark"`
		} `json:"Record"`
	} `json:"DomainRecords"`
}

func (p *aliDNSProvider) call(ctx context.Context, action string, params map[string]string) (aliDNSResponse, error) {
	common := map[string]string{
		"AccessKeyId":      p.accessKeyID,
		"Action":           action,
		"Format":           "JSON",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   fmt.Sprintf("%d-%d", time.Now().UnixNano(), p.credential.ID),
		"SignatureVersion": "1.0",
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2015-01-09",
	}
	for key, value := range params {
		common[key] = value
	}
	canonical := aliCanonicalQuery(common)
	stringToSign := "GET&%2F&" + aliPercentEncode(canonical)
	mac := hmac.New(sha1.New, []byte(p.accessKeySecret+"&")) // #nosec G401 -- required by Alibaba Cloud DNS API v1.
	_, _ = mac.Write([]byte(stringToSign))
	signature := aliPercentEncode(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	apiBase := strings.TrimSpace(p.apiBase)
	if apiBase == "" {
		apiBase = "https://alidns.aliyuncs.com/"
	}
	endpoint := strings.TrimRight(apiBase, "?") + "?Signature=" + signature + "&" + canonical
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return aliDNSResponse{}, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return aliDNSResponse{}, err
	}
	defer resp.Body.Close()
	var out aliDNSResponse
	if err := decodeLimitedJSON(resp.Body, &out); err != nil {
		return out, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out.Code != "" {
		return out, fmt.Errorf("AliDNS API %s failed: %s %s", action, out.Code, out.Message)
	}
	return out, nil
}

func (p *aliDNSProvider) Verify(ctx context.Context) error {
	_, err := p.call(ctx, "DescribeDomainRecords", map[string]string{"DomainName": p.credential.ZoneName, "PageSize": "1"})
	return err
}

func (p *aliDNSProvider) ListRecords(ctx context.Context) ([]model.DNSRecord, error) {
	response, err := p.call(ctx, "DescribeDomainRecords", map[string]string{"DomainName": p.credential.ZoneName, "PageSize": "500"})
	if err != nil {
		return nil, err
	}
	out := make([]model.DNSRecord, 0, len(response.DomainRecords.Records))
	for _, item := range response.DomainRecords.Records {
		record := p.record(item.ID, item.Type, absoluteDNSName(item.RR, p.credential.ZoneName), item.Value, item.TTL, false, strings.EqualFold(item.Status, "ENABLE"))
		record.Comment = item.Remark
		out = append(out, record)
	}
	return out, nil
}

func (p *aliDNSProvider) UpsertRecord(ctx context.Context, record model.DNSRecord) (model.DNSRecord, error) {
	rr, err := relativeDNSName(record.Name, p.credential.ZoneName)
	if err != nil {
		return model.DNSRecord{}, err
	}
	params := map[string]string{"RR": rr, "Type": strings.ToUpper(record.Type), "Value": record.Content, "TTL": strconv.Itoa(normalizeDNSTTL(record.TTL))}
	action := "AddDomainRecord"
	params["DomainName"] = p.credential.ZoneName
	if record.ID != "" {
		action = "UpdateDomainRecord"
		delete(params, "DomainName")
		params["RecordId"] = record.ID
	}
	response, err := p.call(ctx, action, params)
	if err != nil {
		return model.DNSRecord{}, err
	}
	if record.ID == "" {
		record.ID = response.RecordID
	}
	if record.Comment != "" {
		if _, err := p.call(ctx, "UpdateDomainRecordRemark", map[string]string{"RecordId": record.ID, "Remark": record.Comment}); err != nil {
			return model.DNSRecord{}, err
		}
	}
	record.CredentialID, record.ZoneName, record.ZoneID = p.credential.ID, p.credential.ZoneName, p.credential.ZoneID
	record.Name, record.Type, record.TTL, record.Enabled = normalizeDomainName(record.Name), strings.ToUpper(record.Type), normalizeDNSTTL(record.TTL), true
	return record, nil
}

func (p *aliDNSProvider) DeleteRecord(ctx context.Context, id string) error {
	_, err := p.call(ctx, "DeleteDomainRecord", map[string]string{"RecordId": id})
	return err
}

func aliCanonicalQuery(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, aliPercentEncode(key)+"="+aliPercentEncode(values[key]))
	}
	return strings.Join(parts, "&")
}

func aliPercentEncode(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}

type tencentDNSProvider struct {
	dnsProviderBase
	secretID  string
	secretKey string
	endpoint  string
}

func (p *tencentDNSProvider) call(ctx context.Context, action string, payload any, out any) error {
	return tc3Request(ctx, p.httpClient, "dnspod", "dnspod.tencentcloudapi.com", p.endpoint, "2021-03-23", action, p.secretID, p.secretKey, payload, out)
}

func (p *tencentDNSProvider) Verify(ctx context.Context) error {
	var response map[string]any
	return p.call(ctx, "DescribeRecordList", map[string]any{"Domain": p.credential.ZoneName, "Limit": 1}, &response)
}

func (p *tencentDNSProvider) ListRecords(ctx context.Context) ([]model.DNSRecord, error) {
	var response struct {
		Response struct {
			Records []struct {
				ID     int64  `json:"RecordId"`
				Name   string `json:"Name"`
				Type   string `json:"Type"`
				Value  string `json:"Value"`
				TTL    int    `json:"TTL"`
				Status string `json:"Status"`
				Remark string `json:"Remark"`
			} `json:"RecordList"`
		} `json:"Response"`
	}
	if err := p.call(ctx, "DescribeRecordList", map[string]any{"Domain": p.credential.ZoneName, "Limit": 3000}, &response); err != nil {
		return nil, err
	}
	out := make([]model.DNSRecord, 0, len(response.Response.Records))
	for _, item := range response.Response.Records {
		record := p.record(strconv.FormatInt(item.ID, 10), item.Type, absoluteDNSName(item.Name, p.credential.ZoneName), item.Value, item.TTL, false, strings.EqualFold(item.Status, "ENABLE"))
		record.Comment = item.Remark
		out = append(out, record)
	}
	return out, nil
}

func (p *tencentDNSProvider) UpsertRecord(ctx context.Context, record model.DNSRecord) (model.DNSRecord, error) {
	relative, err := relativeDNSName(record.Name, p.credential.ZoneName)
	if err != nil {
		return model.DNSRecord{}, err
	}
	payload := map[string]any{"Domain": p.credential.ZoneName, "SubDomain": relative, "RecordType": strings.ToUpper(record.Type), "RecordLine": "默认", "Value": record.Content, "TTL": normalizeDNSTTL(record.TTL)}
	var response map[string]any
	if record.ID == "" {
		if err := p.call(ctx, "CreateRecord", payload, &response); err != nil {
			return model.DNSRecord{}, err
		}
		record.ID = jsonNumberString(nestedValue(response, "Response", "RecordId"))
	} else {
		recordID, err := strconv.ParseInt(record.ID, 10, 64)
		if err != nil {
			return model.DNSRecord{}, errors.New("invalid DNSPod record id")
		}
		payload["RecordId"] = recordID
		if err := p.call(ctx, "ModifyRecord", payload, &response); err != nil {
			return model.DNSRecord{}, err
		}
	}
	if record.Comment != "" {
		recordID, _ := strconv.ParseInt(record.ID, 10, 64)
		if err := p.call(ctx, "ModifyRecordRemark", map[string]any{"Domain": p.credential.ZoneName, "RecordId": recordID, "Remark": record.Comment}, &response); err != nil {
			return model.DNSRecord{}, err
		}
	}
	record.CredentialID, record.ZoneName, record.ZoneID = p.credential.ID, p.credential.ZoneName, p.credential.ZoneID
	record.Name, record.Type, record.TTL, record.Enabled = normalizeDomainName(record.Name), strings.ToUpper(record.Type), normalizeDNSTTL(record.TTL), true
	return record, nil
}

func (p *tencentDNSProvider) DeleteRecord(ctx context.Context, id string) error {
	recordID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return errors.New("invalid DNSPod record id")
	}
	var response map[string]any
	return p.call(ctx, "DeleteRecord", map[string]any{"Domain": p.credential.ZoneName, "RecordId": recordID}, &response)
}

type tencentESAProvider struct {
	dnsProviderBase
	secretID  string
	secretKey string
	endpoint  string
}

func (p *tencentESAProvider) call(ctx context.Context, action string, payload any, out any) error {
	return tc3Request(ctx, p.httpClient, "teo", "teo.tencentcloudapi.com", p.endpoint, "2022-09-01", action, p.secretID, p.secretKey, payload, out)
}

func (p *tencentESAProvider) Verify(ctx context.Context) error {
	if p.credential.ZoneID == "" {
		return errors.New("tencent ESA requires zone_id")
	}
	var response map[string]any
	return p.call(ctx, "DescribeDnsRecords", map[string]any{"ZoneId": p.credential.ZoneID, "Limit": 1}, &response)
}

func (p *tencentESAProvider) ListRecords(ctx context.Context) ([]model.DNSRecord, error) {
	var response map[string]any
	if err := p.call(ctx, "DescribeDnsRecords", map[string]any{"ZoneId": p.credential.ZoneID, "Limit": 200}, &response); err != nil {
		return nil, err
	}
	items, _ := nestedValue(response, "Response", "DnsRecords").([]any)
	out := make([]model.DNSRecord, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		out = append(out, p.record(jsonNumberString(item["RecordId"]), stringValueAny(item["Type"]), stringValueAny(item["Name"]), stringValueAny(item["Content"]), intValueAny(item["TTL"]), false, !strings.EqualFold(stringValueAny(item["Status"]), "disable")))
	}
	return out, nil
}

func (p *tencentESAProvider) UpsertRecord(ctx context.Context, record model.DNSRecord) (model.DNSRecord, error) {
	if record.ID != "" {
		if err := p.DeleteRecord(ctx, record.ID); err != nil {
			return model.DNSRecord{}, err
		}
		record.ID = ""
	}
	payload := map[string]any{"ZoneId": p.credential.ZoneID, "Name": normalizeDomainName(record.Name), "Type": strings.ToUpper(record.Type), "Content": record.Content, "TTL": normalizeDNSTTL(record.TTL)}
	var response map[string]any
	if err := p.call(ctx, "CreateDnsRecord", payload, &response); err != nil {
		return model.DNSRecord{}, err
	}
	record.ID = jsonNumberString(nestedValue(response, "Response", "RecordId"))
	record.CredentialID, record.ZoneName, record.ZoneID = p.credential.ID, p.credential.ZoneName, p.credential.ZoneID
	record.Name, record.Type, record.TTL, record.Enabled = normalizeDomainName(record.Name), strings.ToUpper(record.Type), normalizeDNSTTL(record.TTL), true
	return record, nil
}

func (p *tencentESAProvider) DeleteRecord(ctx context.Context, id string) error {
	var response map[string]any
	return p.call(ctx, "DeleteDnsRecords", map[string]any{"ZoneId": p.credential.ZoneID, "RecordIds": []string{id}}, &response)
}

func tc3Request(ctx context.Context, client *http.Client, service, host, endpoint, version, action, secretID, secretKey string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	timestamp := time.Now().UTC().Unix()
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	lowerAction := strings.ToLower(action)
	canonicalHeaders := "content-type:application/json\n" + "host:" + host + "\n" + "x-tc-action:" + lowerAction + "\n"
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := "POST\n/\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + sha256HexBytes(body)
	credentialScope := date + "/" + service + "/tc3_request"
	stringToSign := "TC3-HMAC-SHA256\n" + strconv.FormatInt(timestamp, 10) + "\n" + credentialScope + "\n" + sha256HexBytes([]byte(canonicalRequest))
	secretDate := hmacSHA256([]byte("TC3"+secretKey), []byte(date))
	secretService := hmacSHA256(secretDate, []byte(service))
	secretSigning := hmacSHA256(secretService, []byte("tc3_request"))
	signature := hex.EncodeToString(hmacSHA256(secretSigning, []byte(stringToSign)))
	authorization := "TC3-HMAC-SHA256 Credential=" + secretID + "/" + credentialScope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	if strings.TrimSpace(endpoint) == "" {
		endpoint = "https://" + host
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("Authorization", authorization)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("tencent cloud API returned HTTP %d", resp.StatusCode)
	}
	if apiError, _ := nestedValue(envelope, "Response", "Error").(map[string]any); apiError != nil {
		return fmt.Errorf("tencent cloud API %s failed: %s %s", action, stringValueAny(apiError["Code"]), stringValueAny(apiError["Message"]))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tencent cloud API %s returned HTTP %d", action, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func hmacSHA256(key, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}

func sha256HexBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

type huaweiDNSProvider struct {
	dnsProviderBase
	username    string
	password    string
	domainName  string
	region      string
	token       string
	iamEndpoint string
	dnsEndpoint string
}

func (p *huaweiDNSProvider) authenticate(ctx context.Context) error {
	if !validCloudRegion(p.region) {
		return errors.New("invalid Huawei Cloud region")
	}
	body := map[string]any{"auth": map[string]any{"identity": map[string]any{"methods": []string{"password"}, "password": map[string]any{"user": map[string]any{"name": p.username, "password": p.password, "domain": map[string]any{"name": p.domainName}}}}, "scope": map[string]any{"project": map[string]any{"name": p.region}}}}
	data, _ := json.Marshal(body)
	iamEndpoint := strings.TrimSpace(p.iamEndpoint)
	if iamEndpoint == "" {
		iamEndpoint = "https://iam.myhuaweicloud.com/v3/auth/tokens"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, iamEndpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("huawei cloud IAM returned HTTP %d", resp.StatusCode)
	}
	p.token = strings.TrimSpace(resp.Header.Get("X-Subject-Token"))
	if p.token == "" {
		return errors.New("huawei cloud IAM did not return a token")
	}
	return nil
}

func (p *huaweiDNSProvider) dnsRequest(ctx context.Context, method, path string, body any, out any) error {
	if p.token == "" {
		if err := p.authenticate(ctx); err != nil {
			return err
		}
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	dnsEndpoint := strings.TrimRight(strings.TrimSpace(p.dnsEndpoint), "/")
	if dnsEndpoint == "" {
		dnsEndpoint = "https://dns." + p.region + ".myhuaweicloud.com"
	}
	endpoint := dnsEndpoint + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Token", p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("huawei cloud DNS returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return decodeLimitedJSON(resp.Body, out)
}

func (p *huaweiDNSProvider) zoneID(ctx context.Context) (string, error) {
	if p.credential.ZoneID != "" {
		return p.credential.ZoneID, nil
	}
	var response struct {
		Zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"zones"`
	}
	query := url.Values{"name": []string{strings.TrimSuffix(p.credential.ZoneName, ".") + "."}}
	if err := p.dnsRequest(ctx, http.MethodGet, "/v2/zones?"+query.Encode(), nil, &response); err != nil {
		return "", err
	}
	for _, zone := range response.Zones {
		if strings.EqualFold(strings.TrimSuffix(zone.Name, "."), strings.TrimSuffix(p.credential.ZoneName, ".")) {
			return zone.ID, nil
		}
	}
	return "", fmt.Errorf("huawei cloud DNS zone %s not found", p.credential.ZoneName)
}

func (p *huaweiDNSProvider) Verify(ctx context.Context) error {
	_, err := p.zoneID(ctx)
	return err
}

func (p *huaweiDNSProvider) ListRecords(ctx context.Context) ([]model.DNSRecord, error) {
	zoneID, err := p.zoneID(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Recordsets []struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Type        string   `json:"type"`
			TTL         int      `json:"ttl"`
			Records     []string `json:"records"`
			Status      string   `json:"status"`
			Description string   `json:"description"`
		} `json:"recordsets"`
	}
	if err := p.dnsRequest(ctx, http.MethodGet, "/v2/zones/"+url.PathEscape(zoneID)+"/recordsets?limit=500", nil, &response); err != nil {
		return nil, err
	}
	var out []model.DNSRecord
	for _, set := range response.Recordsets {
		for _, value := range set.Records {
			record := p.record(set.ID, set.Type, strings.TrimSuffix(set.Name, "."), normalizeHuaweiRecordValue(set.Type, value), set.TTL, false, strings.EqualFold(set.Status, "ACTIVE"))
			record.Comment = set.Description
			out = append(out, record)
		}
	}
	return out, nil
}

func (p *huaweiDNSProvider) UpsertRecord(ctx context.Context, record model.DNSRecord) (model.DNSRecord, error) {
	zoneID, err := p.zoneID(ctx)
	if err != nil {
		return model.DNSRecord{}, err
	}
	value := record.Content
	if strings.EqualFold(record.Type, "TXT") {
		value = strconv.Quote(value)
	}
	payload := map[string]any{"name": normalizeDomainName(record.Name) + ".", "type": strings.ToUpper(record.Type), "ttl": normalizeDNSTTL(record.TTL), "records": []string{value}, "description": record.Comment}
	method := http.MethodPost
	path := "/v2/zones/" + url.PathEscape(zoneID) + "/recordsets"
	if record.ID != "" {
		method = http.MethodPut
		path += "/" + url.PathEscape(record.ID)
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := p.dnsRequest(ctx, method, path, payload, &saved); err != nil {
		return model.DNSRecord{}, err
	}
	if saved.ID != "" {
		record.ID = saved.ID
	}
	record.CredentialID, record.ZoneName, record.ZoneID = p.credential.ID, p.credential.ZoneName, zoneID
	record.Name, record.Type, record.TTL, record.Enabled = normalizeDomainName(record.Name), strings.ToUpper(record.Type), normalizeDNSTTL(record.TTL), true
	return record, nil
}

func (p *huaweiDNSProvider) addTXTValue(ctx context.Context, record model.DNSRecord, existing []model.DNSRecord) (func(context.Context) error, error) {
	if !strings.EqualFold(record.Type, "TXT") {
		saved, err := p.UpsertRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		return func(cleanupCtx context.Context) error { return p.DeleteRecord(cleanupCtx, saved.ID) }, nil
	}
	original := make([]string, 0)
	recordID := ""
	for _, item := range existing {
		if strings.EqualFold(item.Type, "TXT") && normalizeDomainName(item.Name) == normalizeDomainName(record.Name) {
			if recordID == "" {
				recordID = item.ID
			}
			if item.ID == recordID {
				original = append(original, item.Content)
			}
		}
	}
	if recordID == "" {
		saved, err := p.UpsertRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		return func(cleanupCtx context.Context) error { return p.DeleteRecord(cleanupCtx, saved.ID) }, nil
	}
	values := append(append([]string(nil), original...), record.Content)
	if err := p.putRecordValues(ctx, recordID, record.Name, "TXT", record.TTL, values); err != nil {
		return nil, err
	}
	return func(cleanupCtx context.Context) error {
		return p.putRecordValues(cleanupCtx, recordID, record.Name, "TXT", record.TTL, original)
	}, nil
}

func (p *huaweiDNSProvider) putRecordValues(ctx context.Context, recordID, name, recordType string, ttl int, values []string) error {
	zoneID, err := p.zoneID(ctx)
	if err != nil {
		return err
	}
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		if strings.EqualFold(recordType, "TXT") {
			value = strconv.Quote(value)
		}
		encoded = append(encoded, value)
	}
	payload := map[string]any{"name": normalizeDomainName(name) + ".", "type": strings.ToUpper(recordType), "ttl": normalizeDNSTTL(ttl), "records": encoded}
	path := "/v2/zones/" + url.PathEscape(zoneID) + "/recordsets/" + url.PathEscape(recordID)
	return p.dnsRequest(ctx, http.MethodPut, path, payload, nil)
}

func (p *huaweiDNSProvider) DeleteRecord(ctx context.Context, id string) error {
	zoneID, err := p.zoneID(ctx)
	if err != nil {
		return err
	}
	return p.dnsRequest(ctx, http.MethodDelete, "/v2/zones/"+url.PathEscape(zoneID)+"/recordsets/"+url.PathEscape(id), nil, nil)
}

func validCloudRegion(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func normalizeHuaweiRecordValue(recordType, value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(recordType, "TXT") {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return strings.TrimSuffix(value, ".")
}

func normalizeDNSTTL(ttl int) int {
	if ttl <= 0 {
		return 300
	}
	return ttl
}

func relativeDNSName(name, zone string) (string, error) {
	name = normalizeDomainName(name)
	zone = normalizeDomainName(zone)
	if name == zone {
		return "@", nil
	}
	suffix := "." + zone
	if !strings.HasSuffix(name, suffix) {
		return "", fmt.Errorf("record %s is outside DNS zone %s", name, zone)
	}
	return strings.TrimSuffix(name, suffix), nil
}

func absoluteDNSName(relative, zone string) string {
	relative = strings.TrimSuffix(strings.TrimSpace(relative), ".")
	zone = normalizeDomainName(zone)
	if relative == "" || relative == "@" {
		return zone
	}
	if strings.HasSuffix(normalizeDomainName(relative), "."+zone) {
		return normalizeDomainName(relative)
	}
	return normalizeDomainName(relative + "." + zone)
}

func decodeLimitedJSON(reader io.Reader, out any) error {
	return json.NewDecoder(io.LimitReader(reader, 2<<20)).Decode(out)
}

func nestedValue(value map[string]any, path ...string) any {
	var current any = value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = object[key]
	}
	return current
}

func jsonNumberString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func stringValueAny(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func intValueAny(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}
