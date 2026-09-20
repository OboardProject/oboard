package pluginui

import "testing"

func TestParse(t *testing.T) {
	good := `{"pages":[{"id":"overview","title":"巡检","components":[{"id":"intro","type":"text","text":"状态"},{"id":"run","type":"form","fields":[{"name":"server_id","label":"服务器","type":"text","required":true}],"action":{"label":"检查","params":{"operation":"inspect"}}},{"id":"results","type":"table","columns":[{"key":"status","label":"状态"}],"action":{"label":"刷新","params":{"operation":"list"}}}]}]}`
	doc, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) != 1 {
		t.Fatal("missing page")
	}
}
func TestRejectUnsafeOrAmbiguousUI(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `{"pages":[]}`, `{"pages":[]} {}`,
		`{"pages":[{"id":"overview","title":"概览"}]}`,
		`{"pages":[{"id":"overview","title":"概览","components":null}]}`,
		`{"pages":[{"id":"a","title":"A","html":"<script/>","components":[]}]}`,
		`{"pages":[{"id":"a","title":"A","components":[{"id":"x","type":"iframe"}]}]}`,
		`{"pages":[{"id":"a","title":"A","components":[]},{"id":"a","title":"B","components":[]}]}`,
		`{"pages":[{"id":"a","title":"A","components":[{"id":"x","type":"form","fields":[{"name":"operation","label":"op","type":"text"}],"action":{"label":"Run","params":{"operation":"fixed"}}}]}]}`,
		`{"pages":[{"id":"a","title":"A","components":[{"id":"x","type":"button","action":{"label":"Run","url":"https://example.com"}}]}]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
