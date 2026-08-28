package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestDetectSubscriptionClientTable(t *testing.T) {
	tests := []struct {
		ua       string
		format   model.SubscriptionFormat
		name     string
		matched  bool
		ruleID   string
	}{
		{ua: "clash-meta/1.19.0", format: model.SubscriptionFormatMihomo, name: "Clash Meta", matched: true, ruleID: "clash-meta"},
		{ua: "Clash.Meta/v1", format: model.SubscriptionFormatMihomo, name: "Clash Meta", matched: true, ruleID: "clash-meta"},
		{ua: "mihomo/alpha", format: model.SubscriptionFormatMihomo, name: "Mihomo", matched: true, ruleID: "mihomo"},
		{ua: "Clash Verge Rev/2.0", format: model.SubscriptionFormatMihomo, name: "Clash Verge", matched: true, ruleID: "clash-verge"},
		{ua: "Clash Verge/1.0", format: model.SubscriptionFormatMihomo, name: "Clash Verge", matched: true, ruleID: "clash-verge"},
		{ua: "clash-nyanpasu/1", format: model.SubscriptionFormatMihomo, name: "Clash Nyanpasu", matched: true, ruleID: "nyanpasu"},
		{ua: "FlClash/0.8", format: model.SubscriptionFormatMihomo, name: "FlClash", matched: true, ruleID: "flclash"},
		{ua: "ClashX Meta/1.4", format: model.SubscriptionFormatMihomo, name: "ClashX Meta", matched: true, ruleID: "clashx-meta"},
		{ua: "Clash/1.18.0", format: model.SubscriptionFormatMihomo, name: "Clash", matched: true, ruleID: "clash"},
		{ua: "Surge/5.8.1 Mac", format: model.SubscriptionFormatSurgeMac, name: "Surge Mac", matched: true, ruleID: "surge-mac"},
		{ua: "Surge Mac/5.0", format: model.SubscriptionFormatSurgeMac, name: "Surge Mac", matched: true, ruleID: "surge-mac"},
		{ua: "Surge iOS/5.8.0", format: model.SubscriptionFormatSurge, name: "Surge", matched: true, ruleID: "surge"},
		{ua: "Stash/2.0", format: model.SubscriptionFormatStash, name: "Stash", matched: true, ruleID: "stash"},
		{ua: "Shadowrocket/2.2", format: model.SubscriptionFormatShadowrocket, name: "Shadowrocket", matched: true, ruleID: "shadowrocket"},
		{ua: "Loon/3.0", format: model.SubscriptionFormatLoon, name: "Loon", matched: true, ruleID: "loon"},
		{ua: "Egern/1.0", format: model.SubscriptionFormatEgern, name: "Egern", matched: true, ruleID: "egern"},
		{ua: "Quantumult%20X/1.4", format: model.SubscriptionFormatQX, name: "Quantumult X", matched: true, ruleID: "quantumult-x"},
		{ua: "Surfboard/2.0", format: model.SubscriptionFormatSurfboard, name: "Surfboard", matched: true, ruleID: "surfboard"},
		{ua: "sing-box/1.11", format: model.SubscriptionFormatSingBox, name: "sing-box", matched: true, ruleID: "sing-box"},
		{ua: "SFA/1.11.0", format: model.SubscriptionFormatSingBox, name: "SFA", matched: true, ruleID: "sfa"},
		{ua: "SFI/1.11.0", format: model.SubscriptionFormatSingBox, name: "SFI", matched: true, ruleID: "sfi"},
		{ua: "SFM/1.11.0", format: model.SubscriptionFormatSingBox, name: "SFM", matched: true, ruleID: "sfm"},
		{ua: "v2rayNG/1.8", format: model.SubscriptionFormatV2Ray, name: "v2rayNG", matched: true, ruleID: "v2rayng"},
		{ua: "v2rayN/6.0", format: model.SubscriptionFormatV2Ray, name: "v2rayN", matched: true, ruleID: "v2rayn"},
		{ua: "mieru/2.0", format: model.SubscriptionFormatMihomo, name: "Mieru", matched: false, ruleID: "mieru"},
		{ua: "curl/8.7.1", format: model.SubscriptionFormatMihomo, matched: false, ruleID: "unknown"},
		{ua: "", format: model.SubscriptionFormatMihomo, matched: false, ruleID: "unknown"},
		{ua: "UnknownDownloader/9", format: model.SubscriptionFormatMihomo, matched: false, ruleID: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.ua+"/"+test.ruleID, func(t *testing.T) {
			got := DetectSubscriptionClient(test.ua)
			if got.ResolvedFormat != test.format || got.Matched != test.matched || got.RuleID != test.ruleID {
				t.Fatalf("match=%#v want format=%s matched=%v rule=%s", got, test.format, test.matched, test.ruleID)
			}
			if test.name != "" && got.ClientName != test.name {
				t.Fatalf("client name = %q want %q", got.ClientName, test.name)
			}
		})
	}
}

func TestResolveSubscriptionFormatEmptyStaysSingBox(t *testing.T) {
	resolution := ResolveSubscriptionFormat("", "Surge iOS/5.8.0")
	if resolution.Requested != model.SubscriptionFormatSingBox || resolution.Resolved != model.SubscriptionFormatSingBox || resolution.Auto {
		t.Fatalf("bare URL resolution = %#v", resolution)
	}
}

func TestResolveSubscriptionFormatExplicitWinsOverUA(t *testing.T) {
	resolution := ResolveSubscriptionFormat(model.SubscriptionFormatMihomo, "Surge iOS/5.8.0")
	if resolution.Requested != model.SubscriptionFormatMihomo || resolution.Resolved != model.SubscriptionFormatMihomo || resolution.Auto {
		t.Fatalf("explicit format lost to UA: %#v", resolution)
	}
}

func TestResolveSubscriptionFormatAutoUsesUA(t *testing.T) {
	resolution := ResolveSubscriptionFormat(model.SubscriptionFormatAuto, "Surge iOS/5.8.0")
	if !resolution.Auto || resolution.Requested != model.SubscriptionFormatAuto || resolution.Resolved != model.SubscriptionFormatSurge {
		t.Fatalf("auto resolution = %#v", resolution)
	}
	unknown := ResolveSubscriptionFormat(model.SubscriptionFormatAuto, "curl/8.7.1")
	if !unknown.Auto || unknown.Resolved != model.SubscriptionFormatMihomo || unknown.Match.Matched {
		t.Fatalf("unknown auto resolution = %#v", unknown)
	}
}

func TestRenderSubscriptionTargetRejectsAuto(t *testing.T) {
	_, err := renderSubscriptionTarget(nil, model.SubscriptionFormatAuto)
	if err == nil {
		t.Fatal("auto must not reach a renderer")
	}
}
