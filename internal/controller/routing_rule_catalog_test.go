package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/ruleset"
)

func TestBlackmatrixCatalogSearchReturnsDomainRuleFiles(t *testing.T) {
	blackmatrixCatalogCache.at = time.Now()
	blackmatrixCatalogCache.items = []routingRuleCatalogItem{
		{Name: "Advertising", Path: "source/rule/Advertising/Advertising.list", Category: "source · Advertising", Format: model.RoutingRuleSetFormatBlackmatrixClassical},
		{Name: "Google", Path: "rule/Surge/Google/Google.list", Category: "Surge · Google", Format: model.RoutingRuleSetFormatBlackmatrixClassical},
	}
	server := &Server{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/routing-rule-catalog?q=google", nil)
	server.routingRuleCatalog(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Items []routingRuleCatalogItem `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].Name != "Google" || response.Items[0].Category != "Surge · Google" {
		t.Fatalf("items=%#v", response.Items)
	}
}

func TestBlackmatrixClassicalConversionSkipsNonDomainRules(t *testing.T) {
	content := []byte("payload:\n  - DOMAIN-SUFFIX,example.com\n  - PROCESS-NAME,curl\n")
	converted, err := ruleset.Convert(model.RoutingRuleSetFormatBlackmatrixClassical, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) == 0 {
		t.Fatal("expected converted domain rules")
	}
}
