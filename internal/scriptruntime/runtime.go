package scriptruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/OboardProject/oboard/internal/scripting"
	"github.com/OboardProject/oboard/internal/scriptrpc"
)

type SDKFunc func(capability string, arguments json.RawMessage, actionKey string) (scriptrpc.SDKResponse, error)
type LogFunc func(level, message string, fields json.RawMessage)

func Execute(source string, params json.RawMessage, env map[string]string, limits scriptrpc.RunLimits, invoke SDKFunc, log LogFunc) (json.RawMessage, error) {
	if err := scripting.ValidateSource(source); err != nil {
		return nil, err
	}
	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))
	deadline := time.Now().Add(time.Duration(limits.TimeoutSeconds) * time.Second)
	vm.SetMaxCallStackSize(128)
	interrupt := time.AfterFunc(time.Until(deadline), func() { vm.Interrupt("script timed out") })
	defer interrupt.Stop()

	logsBytes := int64(0)
	sdkCalls := 0
	oboard := map[string]any{}
	for _, name := range []string{
		"servers.get", "servers.list", "servers.status",
		"metrics.latest", "incidents.get", "services.status", "services.restart",
		"host.poweroff", "host.reboot", "notifications.send",
		"operations.get", "operations.wait", "state.get", "state.compareAndSet",
	} {
		capability := name
		oboard[jsName(capability)] = func(call goja.FunctionCall) goja.Value {
			sdkCalls++
			if sdkCalls > limits.SDKCalls {
				panic(vm.ToValue(map[string]any{"error_code": "limit_exceeded", "message": "SDK call limit exceeded"}))
			}
			args := json.RawMessage(`{}`)
			if len(call.Arguments) > 0 {
				exported := call.Arguments[0].Export()
				raw, err := json.Marshal(exported)
				if err != nil {
					panic(vm.ToValue(map[string]any{"error_code": "invalid_input", "message": err.Error()}))
				}
				args = raw
			}
			resp, err := invoke(capability, args, fmt.Sprintf("%s:%d", capability, sdkCalls))
			if err != nil {
				panic(vm.ToValue(map[string]any{"error_code": "internal_error", "message": err.Error()}))
			}
			if !resp.OK {
				panic(vm.ToValue(map[string]any{"error_code": resp.ErrorCode, "message": resp.Message}))
			}
			var decoded any
			if len(resp.Result) > 0 {
				_ = json.Unmarshal(resp.Result, &decoded)
			}
			out := map[string]any{"ok": true, "result": decoded}
			if resp.OperationID != "" {
				out["operation_id"] = resp.OperationID
			}
			return vm.ToValue(out)
		}
	}
	if err := vm.Set("oboard", nestSDK(oboard)); err != nil {
		return nil, err
	}
	logFn := func(level string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			msg := ""
			if len(call.Arguments) > 0 {
				msg = call.Arguments[0].String()
			}
			if int64(len(msg))+logsBytes > limits.LogBytes {
				panic(vm.ToValue(map[string]any{"error_code": "limit_exceeded", "message": "log budget exceeded"}))
			}
			logsBytes += int64(len(msg))
			if log != nil {
				log(level, sanitizeLog(msg), nil)
			}
			return goja.Undefined()
		}
	}
	_ = vm.Set("log", map[string]any{"info": logFn("info"), "warn": logFn("warn"), "error": logFn("error")})
	var decodedParams any
	if len(params) > 0 {
		_ = json.Unmarshal(params, &decodedParams)
	}
	_ = vm.Set("params", decodedParams)
	_ = vm.Set("env", env)
	if _, err := vm.RunString(source); err != nil {
		return nil, err
	}
	main, ok := goja.AssertFunction(vm.Get("main"))
	if !ok {
		return nil, errors.New("entry must be function main")
	}
	value, err := main(goja.Undefined())
	if err != nil {
		return nil, err
	}
	exported := value.Export()
	raw, err := json.Marshal(exported)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limits.ResultBytes {
		return nil, errors.New("result exceeds 64 KiB")
	}
	return raw, nil
}

func jsName(capability string) string {
	return capability
}

func nestSDK(flat map[string]any) map[string]any {
	root := map[string]any{}
	for name, fn := range flat {
		parts := strings.Split(name, ".")
		cursor := root
		for i, part := range parts {
			if i == len(parts)-1 {
				cursor[part] = fn
				continue
			}
			next, _ := cursor[part].(map[string]any)
			if next == nil {
				next = map[string]any{}
				cursor[part] = next
			}
			cursor = next
		}
	}
	return root
}

func sanitizeLog(message string) string {
	message = strings.ReplaceAll(message, "\x1b", "")
	if len(message) > 4000 {
		return message[:4000]
	}
	return message
}
