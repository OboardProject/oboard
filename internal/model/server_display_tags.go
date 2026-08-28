package model

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MaxServerDisplayTags    = 8
	MaxServerDisplayTagText = 24
)

// ServerDisplayTag is an operator-authored label shown on the server card footer.
type ServerDisplayTag struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

func NormalizeServerDisplayTags(tags []ServerDisplayTag) ([]ServerDisplayTag, error) {
	if len(tags) > MaxServerDisplayTags {
		return nil, errors.New("最多 8 个卡片标签")
	}
	out := make([]ServerDisplayTag, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		text := strings.TrimSpace(tag.Text)
		if text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > MaxServerDisplayTagText {
			return nil, errors.New("标签文字最多 24 个字符")
		}
		key := strings.ToLower(text)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ServerDisplayTag{Text: text, Tone: NormalizeServerDisplayTagTone(tag.Tone)})
	}
	return out, nil
}

func NormalizeServerDisplayTagTone(tone string) string {
	switch strings.ToLower(strings.TrimSpace(tone)) {
	case "orange":
		return "orange"
	case "purple":
		return "purple"
	case "green":
		return "green"
	case "gray", "grey":
		return "gray"
	default:
		return "blue"
	}
}
