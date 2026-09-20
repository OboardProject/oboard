package model

import (
	"encoding/json"
	"fmt"
)

// ConfigurationSyncProblem describes Controller-local preparation, not an Agent result.
type ConfigurationSyncProblem struct {
	Code        string                      `json:"code"`
	Category    string                      `json:"category"`
	Resources   []ConfigurationSyncResource `json:"resources"`
	RetryPolicy string                      `json:"retry_policy"`
	Message     string                      `json:"message"`
}

type ConfigurationSyncResource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func EncodeConfigurationSyncProblems(problems []ConfigurationSyncProblem) (string, error) {
	if len(problems) > 16 {
		return "", fmt.Errorf("too many configuration sync problems")
	}
	if problems == nil {
		problems = []ConfigurationSyncProblem{}
	}
	for _, p := range problems {
		if p.Code == "" || len(p.Code) > 96 || len(p.Category) > 32 || len(p.RetryPolicy) > 32 || len(p.Message) > 2000 || len(p.Resources) > 16 {
			return "", fmt.Errorf("invalid configuration sync problem")
		}
		for _, r := range p.Resources {
			if r.Type == "" || r.ID == "" || len(r.Type) > 64 || len(r.ID) > 128 {
				return "", fmt.Errorf("invalid configuration sync resource")
			}
		}
	}
	data, err := json.Marshal(problems)
	return string(data), err
}
