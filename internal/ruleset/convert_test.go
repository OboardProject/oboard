package ruleset

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConvertMihomoFormats(t *testing.T) {
	tests := []struct {
		format string
		body   string
		field  string
	}{
		{model.RoutingRuleSetFormatMihomoDomain, "payload:\n  - +.example.com\n  - exact.example\n", "domain_suffix"},
		{model.RoutingRuleSetFormatMihomoIPCIDR, "payload:\n  - 203.0.113.0/24,no-resolve\n", "ip_cidr"},
		{model.RoutingRuleSetFormatMihomoClassical, "payload:\n  - DOMAIN-SUFFIX,example.com\n  - IP-CIDR,203.0.113.0/24,no-resolve\n", "domain_suffix"},
	}
	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			converted, err := Convert(test.format, []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			var document sourceDocument
			if err := json.Unmarshal(converted, &document); err != nil {
				t.Fatal(err)
			}
			if document.Version != 1 || len(document.Rules) == 0 || document.Rules[0][test.field] == nil {
				t.Fatalf("unexpected converted document: %s", converted)
			}
		})
	}
}

func TestConvertBlackmatrixClassicalSkipsNonDomainRules(t *testing.T) {
	converted, err := Convert(model.RoutingRuleSetFormatBlackmatrixClassical, []byte("payload:\n  - DOMAIN-SUFFIX,example.com\n  - PROCESS-NAME,curl\n  - URL-REGEX,^https://.*,REJECT\n"))
	if err != nil {
		t.Fatal(err)
	}
	var document sourceDocument
	if err := json.Unmarshal(converted, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Rules) != 1 || document.Rules[0]["domain_suffix"] == nil {
		t.Fatalf("converted rules = %s", converted)
	}
}

func TestConvertMihomoFailsStrictlyWithLine(t *testing.T) {
	_, err := Convert(model.RoutingRuleSetFormatMihomoClassical, []byte("payload:\n  - DOMAIN,example.com\n  - PROCESS-NAME,curl\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "PROCESS-NAME") {
		t.Fatalf("expected line-numbered strict error, got %v", err)
	}
}

func TestConvertSingBoxSourceCanonicalizes(t *testing.T) {
	converted, err := Convert(model.RoutingRuleSetFormatSingBoxSource, []byte(`{ "rules": [{"domain":["example.com"]}], "version": 1 }`))
	if err != nil {
		t.Fatal(err)
	}
	if string(converted) != `{"version":1,"rules":[{"domain":["example.com"]}]}` {
		t.Fatalf("unexpected canonical JSON %s", converted)
	}
}
