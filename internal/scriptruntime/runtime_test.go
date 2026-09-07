package scriptruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/scriptrpc"
)

func TestExecuteCallsMainAndBlocksRequire(t *testing.T) {
	calls := 0
	result, err := Execute(`function main(){ var status = oboard.servers.status({server_id:"7"}); log.info("ran"); return {ok:true, server:status.result}; }`, json.RawMessage(`{}`), map[string]string{"OBOARD_RUN_MODE": "simulate"}, scriptrpc.RunLimits{TimeoutSeconds: 2, SDKCalls: 8, LogBytes: 4096, ResultBytes: 4096}, func(capability string, arguments json.RawMessage, _ string) (scriptrpc.SDKResponse, error) {
		calls++
		if capability != "servers.status" || !strings.Contains(string(arguments), `"7"`) {
			t.Fatalf("unexpected SDK call %s %s", capability, arguments)
		}
		return scriptrpc.SDKResponse{OK: true, Result: json.RawMessage(`{"server_id":"7","agent_connected":"unknown"}`)}, nil
	}, func(string, string, json.RawMessage) {})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(string(result), `"ok":true`) {
		t.Fatalf("result=%s calls=%d", result, calls)
	}
	if _, err := Execute(`require("fs")`, nil, nil, scriptrpc.RunLimits{TimeoutSeconds: 1, SDKCalls: 1, ResultBytes: 1024}, nil, nil); err == nil {
		t.Fatal("require must not exist")
	}
}
