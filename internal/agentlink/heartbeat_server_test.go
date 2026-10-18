package agentlink

import (
	"encoding/json"
	"testing"
)

func TestServerHeartbeatDoesNotReflectPong(t *testing.T) {
	testHeartbeat(t, func(s *Session, onMessage func(map[string]json.RawMessage)) error {
		return s.Run(func(envelope map[string]json.RawMessage) error {
			onMessage(envelope)
			return nil
		}, nil)
	})
}
