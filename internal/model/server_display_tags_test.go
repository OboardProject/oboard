package model

import (
	"strings"
	"testing"
)

func TestNormalizeServerDisplayTags(t *testing.T) {
	tags, err := NormalizeServerDisplayTags([]ServerDisplayTag{
		{Text: "  IncuShlii ", Tone: ""},
		{Text: "原生IP/住宅IP", Tone: "orange"},
		{Text: "峰值 500Mbps", Tone: "PURPLE"},
		{Text: "incuShlii", Tone: "green"},
		{Text: "  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 3 {
		t.Fatalf("got %#v", tags)
	}
	if tags[0] != (ServerDisplayTag{Text: "IncuShlii", Tone: "blue"}) {
		t.Fatalf("first tag %#v", tags[0])
	}
	if tags[1].Tone != "orange" || tags[2].Tone != "purple" {
		t.Fatalf("tones %#v", tags)
	}
	tooLong := strings.Repeat("测", MaxServerDisplayTagText+1)
	if _, err := NormalizeServerDisplayTags([]ServerDisplayTag{{Text: tooLong}}); err == nil {
		t.Fatal("expected overflow error")
	}
}
