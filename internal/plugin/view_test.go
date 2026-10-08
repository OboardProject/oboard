package plugin

import (
	"strings"
	"testing"
)

func TestViewDocumentRejectsOpenMarkupAndUndeclaredActions(t *testing.T) {
	page := PageDeclaration{ID: "overview", Title: "巡检", Actions: []string{"recheck"}}
	valid := []byte(`{"title":"巡检","body":[{"type":"metric","label":"丢包","value":"0%","tone":"success"},{"type":"binding","source":"servers.metrics","server":{"$env":"TARGET_SERVER"}},{"type":"button","action":"recheck","label":"立即复测"}]}`)
	canonical, err := ValidateViewDocument(valid, page)
	if err != nil || strings.Contains(string(canonical), "class") {
		t.Fatalf("valid document rejected: %s %v", canonical, err)
	}
	for _, raw := range []string{
		`{"body":[{"type":"html","text":"<b>x</b>"}]}`,
		`{"body":[{"type":"text","text":"ok","class":"x"}]}`,
		`{"body":[{"type":"binding","source":"servers.metrics","server":"2"}]}`,
		`{"body":[{"type":"binding","source":"network.ping","server":{"$env":"TARGET_SERVER"}}]}`,
		`{"body":[{"type":"button","action":"delete","label":"删除"}]}`,
		`{"body":[{"type":"text","text":"ok","href":"https://example.com"}]}`,
	} {
		if _, err := ValidateViewDocument([]byte(raw), page); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("document accepted: %s (%v)", raw, err)
		}
	}
}

func TestPageDeclarationExpandsPermissionReview(t *testing.T) {
	raw := withField(t, traceManifest, "capabilities", []string{"network.trace", "ui.page"})
	raw = withField(t, raw, "pages", []any{map[string]any{"id": "overview", "title": "巡检", "actions": []string{"recheck"}}})
	base := mustManifest(t, raw)
	renamed := base
	renamed.Pages = []PageDeclaration{{ID: "overview", Title: "新标题", Actions: []string{"recheck"}}}
	if diff := DiffPermissions(&base, renamed); diff.Expanded {
		t.Fatalf("title change must not expand permissions: %+v", diff)
	}
	added := base
	added.Pages = []PageDeclaration{{ID: "overview", Title: "巡检", Actions: []string{"recheck", "export"}}, {ID: "health", Title: "健康"}}
	diff := DiffPermissions(&base, added)
	if !diff.Expanded || strings.Join(diff.AddedPages, ",") != "health" || strings.Join(diff.AddedActions, ",") != "overview/export" {
		t.Fatalf("page expansion not detected: %+v", diff)
	}
	if _, err := ParseManifest([]byte(withField(t, traceManifest, "pages", []any{map[string]any{"id": "overview", "title": "巡检"}}))); CodeOf(err) != CodeInvalidManifest {
		t.Fatalf("pages without ui.page accepted: %v", err)
	}
}
