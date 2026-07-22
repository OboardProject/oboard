package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAliDNSProviderRequests(t *testing.T) {
	var actions []string
	var remark string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		actions = append(actions, action)
		if r.URL.Query().Get("AccessKeyId") != "key-id" || r.URL.Query().Get("Signature") == "" {
			http.Error(w, "missing signature", 400)
			return
		}
		switch action {
		case "DescribeDomainRecords":
			_ = json.NewEncoder(w).Encode(map[string]any{"DomainRecords": map[string]any{"Record": []map[string]any{{"RecordId": "record-1", "RR": "www", "Type": "A", "Value": "203.0.113.1", "TTL": 300, "Status": "ENABLE", "Remark": "edge"}}}})
		case "AddDomainRecord":
			_ = json.NewEncoder(w).Encode(map[string]any{"RecordId": "record-2"})
		case "UpdateDomainRecordRemark":
			remark = r.URL.Query().Get("Remark")
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer server.Close()
	provider := &aliDNSProvider{dnsProviderBase: dnsProviderBase{credential: model.DNSCredential{ID: 1, ZoneName: "example.com"}, httpClient: server.Client()}, accessKeyID: "key-id", accessKeySecret: "key-secret", apiBase: server.URL}
	records, err := provider.ListRecords(context.Background())
	if err != nil || len(records) != 1 || records[0].Name != "www.example.com" || records[0].Comment != "edge" {
		t.Fatalf("AliDNS list records=%#v err=%v", records, err)
	}
	saved, err := provider.UpsertRecord(context.Background(), model.DNSRecord{Type: "TXT", Name: "_acme-challenge.example.com", Content: "token", Comment: "OBoard edge", TTL: 60})
	if err != nil || saved.ID != "record-2" || len(actions) != 3 || remark != "OBoard edge" {
		t.Fatalf("AliDNS upsert=%#v actions=%#v err=%v", saved, actions, err)
	}
}

func TestTencentDNSProvidersRequests(t *testing.T) {
	var dnspodRemark string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=secret-id/") || r.Header.Get("X-TC-Timestamp") == "" {
			http.Error(w, "missing TC3 signature", http.StatusUnauthorized)
			return
		}
		switch r.Header.Get("X-TC-Action") {
		case "DescribeRecordList":
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": map[string]any{"RecordList": []map[string]any{{"RecordId": 10, "Name": "www", "Type": "A", "Value": "203.0.113.2", "TTL": 600, "Status": "ENABLE", "Remark": "edge"}}, "RequestId": "request"}})
		case "CreateRecord":
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": map[string]any{"RecordId": 11, "RequestId": "request"}})
		case "ModifyRecordRemark":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			dnspodRemark = stringValueAny(payload["Remark"])
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": map[string]any{"RequestId": "request"}})
		case "DescribeDnsRecords":
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": map[string]any{"DnsRecords": []map[string]any{{"RecordId": "esa-1", "Name": "api.example.com", "Type": "CNAME", "Content": "origin.example.net", "TTL": 300, "Status": "enable"}}, "RequestId": "request"}})
		case "CreateDnsRecord":
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": map[string]any{"RecordId": "esa-2", "RequestId": "request"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"Response": map[string]any{"RequestId": "request"}})
		}
	}))
	defer server.Close()
	base := dnsProviderBase{credential: model.DNSCredential{ID: 2, ZoneName: "example.com", ZoneID: "zone-1"}, httpClient: server.Client()}
	dnspod := &tencentDNSProvider{dnsProviderBase: base, secretID: "secret-id", secretKey: "secret-key", endpoint: server.URL}
	records, err := dnspod.ListRecords(context.Background())
	if err != nil || len(records) != 1 || records[0].ID != "10" || records[0].Name != "www.example.com" || records[0].Comment != "edge" {
		t.Fatalf("DNSPod records=%#v err=%v", records, err)
	}
	savedDNSPod, err := dnspod.UpsertRecord(context.Background(), model.DNSRecord{Type: "A", Name: "edge.example.com", Content: "203.0.113.9", Comment: "OBoard edge", TTL: 300})
	if err != nil || savedDNSPod.ID != "11" || dnspodRemark != "OBoard edge" {
		t.Fatalf("DNSPod saved=%#v remark=%q err=%v", savedDNSPod, dnspodRemark, err)
	}
	esa := &tencentESAProvider{dnsProviderBase: base, secretID: "secret-id", secretKey: "secret-key", endpoint: server.URL}
	records, err = esa.ListRecords(context.Background())
	if err != nil || len(records) != 1 || records[0].ID != "esa-1" || records[0].Content != "origin.example.net" {
		t.Fatalf("ESA records=%#v err=%v", records, err)
	}
	saved, err := esa.UpsertRecord(context.Background(), model.DNSRecord{Type: "TXT", Name: "_acme-challenge.example.com", Content: "token", TTL: 60})
	if err != nil || saved.ID != "esa-2" {
		t.Fatalf("ESA saved=%#v err=%v", saved, err)
	}
}

func TestHuaweiDNSProviderRequests(t *testing.T) {
	var txtValue string
	var description string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v3/auth/tokens":
			w.Header().Set("X-Subject-Token", "iam-token")
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/v2/zones":
			if r.Header.Get("X-Auth-Token") != "iam-token" {
				http.Error(w, "missing token", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"zones": []map[string]any{{"id": "zone-1", "name": "example.com."}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/zones/zone-1/recordsets":
			var payload struct {
				Records     []string `json:"records"`
				Description string   `json:"description"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			description = payload.Description
			if len(payload.Records) > 0 {
				txtValue = payload.Records[0]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "record-1"})
		default:
			http.Error(w, r.URL.Path, 404)
		}
	}))
	defer server.Close()
	provider := &huaweiDNSProvider{dnsProviderBase: dnsProviderBase{credential: model.DNSCredential{ID: 3, ZoneName: "example.com"}, httpClient: server.Client()}, username: "user", password: "password", domainName: "account", region: "cn-north-4", iamEndpoint: server.URL + "/v3/auth/tokens", dnsEndpoint: server.URL}
	if err := provider.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.UpsertRecord(context.Background(), model.DNSRecord{Type: "TXT", Name: "_acme-challenge.example.com", Content: "token", Comment: "OBoard edge", TTL: 60})
	if err != nil || saved.ID != "record-1" || txtValue != `"token"` || description != "OBoard edge" {
		t.Fatalf("Huawei saved=%#v txt=%q err=%v", saved, txtValue, err)
	}
}

func TestHuaweiACMETXTValueRestoresExistingRecordset(t *testing.T) {
	var payloads [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v2/zones/zone-1/recordsets/record-1" {
			http.Error(w, r.URL.Path, 404)
			return
		}
		var payload struct {
			Records []string `json:"records"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		payloads = append(payloads, payload.Records)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()
	provider := &huaweiDNSProvider{dnsProviderBase: dnsProviderBase{credential: model.DNSCredential{ID: 3, ZoneName: "example.com", ZoneID: "zone-1"}, httpClient: server.Client()}, region: "cn-north-4", token: "token", dnsEndpoint: server.URL}
	record := model.DNSRecord{Type: "TXT", Name: "_acme-challenge.example.com", Content: "acme-token", TTL: 60}
	existing := []model.DNSRecord{{ID: "record-1", Type: "TXT", Name: record.Name, Content: "user-value", TTL: 300}}
	cleanup, err := provider.addTXTValue(context.Background(), record, existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 || strings.Join(payloads[0], ",") != `"user-value","acme-token"` || strings.Join(payloads[1], ",") != `"user-value"` {
		t.Fatalf("Huawei TXT recordset payloads = %#v", payloads)
	}
}

func TestCloudflareRecordListingRequestsFullPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "5000" {
			http.Error(w, "missing full page", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{}})
	}))
	defer server.Close()
	client := newCloudflareClient("token", server.URL)
	client.httpClient = server.Client()
	if _, err := client.listDNSRecords(context.Background(), cloudflareZone{ID: "zone-1"}); err != nil {
		t.Fatal(err)
	}
}
