package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

// TestVLESSRecipeDefaultsMatchPanelPreset verifies that the MCP inbound recipe
// fills the panel-equivalent controlled defaults for VLESS Reality without
// asking the model to construct config_json or supply key material.
func TestVLESSRecipeDefaultsMatchPanelPreset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "tokyo", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	prepared, err := server.prepareInboundCreateRecipe(ctx, principal, mcpTaskInput{
		Goal:       "在东京节点创建 VLESS 入口",
		Params:     map[string]any{"protocol": "vless", "port": 443},
		TargetRefs: []string{"server:" + int64String(node.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "ready" {
		t.Fatalf("prepared status = %s", prepared.Status)
	}
	input := prepared.Operations[0].Input
	inbound, _ := input["inbound"].(map[string]any)
	if inbound["certificate_mode"] != "external" {
		t.Fatalf("certificate_mode = %v, want external", inbound["certificate_mode"])
	}
	if tls, _ := inbound["tls"].(bool); tls {
		t.Fatal("tls must default to false for VLESS Reality")
	}
	if inbound["kind"] != "vless-reality" || inbound["config_json"] != `{}` {
		t.Fatalf("controlled Reality fields = %#v", inbound)
	}
	reality, _ := inbound["reality"].(map[string]any)
	if reality["handshake_server"] != defaultVLESSRealityServerName || reality["handshake_port"] != 443 {
		t.Fatalf("Reality defaults = %#v", reality)
	}
}

func TestAnyTLSRecipeLeavesPaddingToCreateInitializer(t *testing.T) {
	var config map[string]any
	if err := json.Unmarshal([]byte(defaultInboundPresetConfig("anytls")), &config); err != nil {
		t.Fatal(err)
	}
	if _, exists := config["padding_scheme"]; exists {
		t.Fatalf("recipe must not preempt Controller create initialization: %#v", config)
	}
}

func TestAnyTLSRecipeFillsManagedCertificateFromDNSDomain(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "OC", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	verified := time.Now()
	credential := &model.DNSCredential{
		Name: "cf", Provider: model.DNSProviderCloudflare, ZoneName: "4805787.xyz", Enabled: true, VerifiedAt: &verified,
		Zones: []model.DNSCredentialZone{{ZoneName: "4805787.xyz"}},
	}
	if err := db.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	prepared, err := server.prepareInboundCreateRecipe(ctx, principal, mcpTaskInput{
		Goal:       "给 OC 创建一个 anytls 节点，同步 dns oc.4805787.xyz",
		Params:     map[string]any{"protocol": "anytls", "port": 443},
		TargetRefs: []string{"server:" + int64String(node.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "ready" {
		t.Fatalf("prepared status = %s questions=%#v", prepared.Status, prepared.Questions)
	}
	inbound, _ := prepared.Operations[0].Input["inbound"].(map[string]any)
	if inbound["certificate_mode"] != "auto" {
		t.Fatalf("certificate_mode = %v, want auto", inbound["certificate_mode"])
	}
	if inbound["dns_domain"] != "oc.4805787.xyz" || inbound["certificate_domain"] != "oc.4805787.xyz" {
		t.Fatalf("domains = %#v", inbound)
	}
	if inbound["dns_sync_enabled"] != true {
		t.Fatalf("dns_sync_enabled = %v", inbound["dns_sync_enabled"])
	}
	if inbound["dns_credential_id"] != credential.ID {
		t.Fatalf("dns_credential_id = %v, want %d", inbound["dns_credential_id"], credential.ID)
	}
}

func TestAnyTLSRecipeNeedsDomainForManagedCertificate(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "OC", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	prepared, err := server.prepareInboundCreateRecipe(ctx, principal, mcpTaskInput{
		Goal:       "给 OC 创建一个 anytls 节点",
		Params:     map[string]any{"protocol": "anytls", "port": 443},
		TargetRefs: []string{"server:" + int64String(node.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "needs_input" {
		t.Fatalf("prepared status = %s, want needs_input", prepared.Status)
	}
	if len(prepared.Questions) == 0 || fmt.Sprint(prepared.Questions[0]["field"]) != "dns_domain" {
		t.Fatalf("questions = %#v", prepared.Questions)
	}
}

// TestSubscriptionPlanListCarriesNodes verifies the subscription-plans list
// resource includes latest and current node sets so MCP clients can confirm
// nodes actually entered a plan.
func TestSubscriptionPlanListCarriesNodes(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "9929", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "Reality", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 15787, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "测试套餐", Enabled: true, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: inbound.ID}}); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	plans, err := server.application.ListSubscriptionPlans(ctx, principal)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans=%#v err=%v", plans, err)
	}
	if len(plans[0].Nodes) != 1 || plans[0].Nodes[0].NodeID != inbound.ID {
		t.Fatalf("list view lost the latest node set: %#v", plans[0].Nodes)
	}
}

// TestAccessChangeResourceSurface verifies the MCP access-change resource
// exposes status, durable error text, retryability, and abandonability for a
// failed release.
func TestAccessChangeResourceSurface(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "测试套餐", Enabled: true, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
		t.Fatal(err)
	}
	changeID, err := db.CreateAccessChange(ctx, &model.AccessChange{
		ChangeType: model.AccessChangePlanPublish, SourcePlanID: plan.ID, Status: model.AccessChangeFailed,
		Error: "activate: database is locked (5) (SQLITE_BUSY)",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	payload, err := server.listAccessChangesMCP(ctx, principal, changeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(payload)
	text := string(encoded)
	for _, needle := range []string{`"status":"failed"`, "database is locked", `"retryable":true`, `"abandonable":false`, `"change_type":"plan_publish"`} {
		if !strings.Contains(text, needle) {
			t.Fatalf("access change payload missing %s: %s", needle, text)
		}
	}
	// Failed records must resolve through the template id extraction as well.
	if got := mcpWorkflowAccessChangeID(nil); got != 0 {
		t.Fatalf("nil workflow access change id = %d", got)
	}
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}
