// Package pluginruntime executes one plugin bundle in Goja. It exposes only
// the OBoard SDK: no require, process, filesystem, sockets, timers, native
// modules or WebAssembly. Every SDK call leaves the runtime as a structured
// request to the Controller capability gateway.
package pluginruntime

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- HMAC-SHA1/SHA1 digests are needed by third-party API signing schemes.
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"

	"github.com/OboardProject/oboard/internal/pluginrpc"
)

//go:embed bootstrap.js
var bootstrapSource string

type CallFunc func(method string, arguments json.RawMessage) pluginrpc.CallResponse
type LogFunc func(level, message string)

// Outcome is the terminal state of one execution.
type Outcome struct {
	Result  json.RawMessage
	Code    string
	Message string
}

const (
	codeRunTimeout     = "RUN_TIMEOUT"
	codeScriptError    = "SCRIPT_ERROR"
	codeResourceLimit  = "RESOURCE_LIMIT"
	codeInvalidArg     = "INVALID_ARGUMENT"
	maxCryptoDataBytes = 1 << 20
)

type timeoutSignal struct{}

// Execute runs source's main(run) to completion or deadline.
func Execute(source string, environment map[string]pluginrpc.EnvValue, runContext pluginrpc.RunContext, limits pluginrpc.Limits, call CallFunc, log LogFunc) Outcome {
	vm := goja.New()
	vm.SetParserOptions(parser.WithDisableSourceMaps)
	vm.SetMaxCallStackSize(256)
	timeout := time.Duration(limits.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		return Outcome{Code: codeRunTimeout, Message: "invalid run deadline"}
	}
	timer := time.AfterFunc(timeout, func() { vm.Interrupt(timeoutSignal{}) })
	defer timer.Stop()

	bootstrap, err := vm.RunScript("oboard-bootstrap.js", bootstrapSource)
	if err != nil {
		return Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "SDK bootstrap failed"}
	}
	start, ok := goja.AssertFunction(bootstrap)
	if !ok {
		return Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "SDK bootstrap failed"}
	}
	envJSON, _ := json.Marshal(environment)
	runJSON, _ := json.Marshal(runContext)
	callNative := func(method, arguments string) string {
		response := call(method, json.RawMessage(arguments))
		raw, _ := json.Marshal(response)
		return string(raw)
	}
	cryptoNative := func(op, arguments string) string {
		result, err := localCrypto(op, []byte(arguments))
		if err != nil {
			raw, _ := json.Marshal(map[string]any{"ok": false, "code": codeInvalidArg, "message": err.Error()})
			return string(raw)
		}
		raw, _ := json.Marshal(map[string]any{"ok": true, "result": result})
		return string(raw)
	}
	logNative := func(level, message string) { log(level, message) }
	runValue, err := start(goja.Undefined(), vm.ToValue(callNative), vm.ToValue(cryptoNative), vm.ToValue(logNative), vm.ToValue(string(envJSON)), vm.ToValue(string(runJSON)))
	if err != nil {
		return outcomeFromError(err)
	}
	if _, err := vm.RunScript("main.js", source); err != nil {
		return outcomeFromError(err)
	}
	main, ok := goja.AssertFunction(vm.Get("main"))
	if !ok {
		return Outcome{Code: codeScriptError, Message: "main.js must declare function main(run)"}
	}
	value, err := main(goja.Undefined(), runValue)
	if err != nil {
		return outcomeFromError(err)
	}
	if promise, ok := value.Export().(*goja.Promise); ok {
		switch promise.State() {
		case goja.PromiseStateFulfilled:
			value = promise.Result()
		case goja.PromiseStateRejected:
			return outcomeFromValue(promise.Result())
		default:
			return Outcome{Code: codeScriptError, Message: "main returned a promise that never settled; timers and background work are not available"}
		}
	}
	result, err := serialize(vm, value)
	if err != nil {
		return Outcome{Code: codeScriptError, Message: "the value returned by main is not JSON-serializable"}
	}
	if limits.ResultBytes > 0 && len(result) > limits.ResultBytes {
		return Outcome{Code: codeResourceLimit, Message: "the value returned by main exceeds the result size limit"}
	}
	return Outcome{Result: result}
}

func serialize(vm *goja.Runtime, value goja.Value) (json.RawMessage, error) {
	if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
		return json.RawMessage("null"), nil
	}
	stringify, ok := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	if !ok {
		return nil, errors.New("JSON.stringify unavailable")
	}
	encoded, err := stringify(goja.Undefined(), value)
	if err != nil {
		return nil, err
	}
	if goja.IsUndefined(encoded) {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(encoded.String()), nil
}

func outcomeFromError(err error) Outcome {
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if _, ok := interrupted.Value().(timeoutSignal); ok {
			return Outcome{Code: codeRunTimeout, Message: "the run exceeded its time limit"}
		}
		return Outcome{Code: "CANCELLED", Message: "the run was interrupted"}
	}
	var exception *goja.Exception
	if errors.As(err, &exception) {
		return outcomeFromValue(exception.Value())
	}
	var stackOverflow *goja.StackOverflowError
	if errors.As(err, &stackOverflow) {
		return Outcome{Code: codeResourceLimit, Message: "maximum call stack size exceeded"}
	}
	return Outcome{Code: codeScriptError, Message: firstLines(err.Error(), 2)}
}

func outcomeFromValue(value goja.Value) Outcome {
	if value == nil {
		return Outcome{Code: codeScriptError, Message: "unknown error"}
	}
	if object, ok := value.(*goja.Object); ok {
		code := ""
		if raw := object.Get("code"); raw != nil && !goja.IsUndefined(raw) {
			code = raw.String()
		}
		message := value.String()
		if raw := object.Get("message"); raw != nil && !goja.IsUndefined(raw) {
			message = raw.String()
		}
		if stack := object.Get("stack"); code == "" && stack != nil && !goja.IsUndefined(stack) {
			message = firstLines(stack.String(), 3)
		}
		if code == "" {
			code = codeScriptError
		}
		return Outcome{Code: code, Message: firstLines(message, 3)}
	}
	return Outcome{Code: codeScriptError, Message: firstLines(value.String(), 2)}
}

func firstLines(text string, n int) string {
	lines := strings.SplitN(text, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(out) > 800 {
		out = out[:800]
	}
	return strings.ToValidUTF8(out, "?")
}

// localCrypto implements the pure computation half of oboard.crypto. HMAC
// with a SecretRef key is never computed here: it is sent to the gateway.
func localCrypto(op string, raw []byte) (any, error) {
	var args struct {
		Algorithm    string `json:"algorithm"`
		Key          string `json:"key"`
		KeyEncoding  string `json:"key_encoding"`
		Data         string `json:"data"`
		DataEncoding string `json:"data_encoding"`
		Output       string `json:"output"`
		URL          bool   `json:"url"`
		Length       int    `json:"length"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, errors.New("invalid crypto arguments")
	}
	if len(args.Data) > maxCryptoDataBytes || len(args.Key) > 64<<10 {
		return nil, errors.New("crypto input exceeds 1 MiB")
	}
	switch op {
	case "hash":
		constructor, err := hashFor(args.Algorithm)
		if err != nil {
			return nil, err
		}
		data, err := decodeInput(args.Data, args.DataEncoding)
		if err != nil {
			return nil, err
		}
		h := constructor()
		h.Write(data)
		return encodeDigest(h.Sum(nil), args.Output)
	case "hmac":
		constructor, err := hashFor(args.Algorithm)
		if err != nil {
			return nil, err
		}
		key, err := decodeInput(args.Key, args.KeyEncoding)
		if err != nil {
			return nil, err
		}
		data, err := decodeInput(args.Data, args.DataEncoding)
		if err != nil {
			return nil, err
		}
		mac := hmac.New(constructor, key)
		mac.Write(data)
		return encodeDigest(mac.Sum(nil), args.Output)
	case "base64_encode":
		data, err := decodeInput(args.Data, args.DataEncoding)
		if err != nil {
			return nil, err
		}
		if args.URL {
			return base64.RawURLEncoding.EncodeToString(data), nil
		}
		return base64.StdEncoding.EncodeToString(data), nil
	case "base64_decode":
		encoding := base64.StdEncoding
		if args.URL {
			encoding = base64.RawURLEncoding
		}
		data, err := encoding.DecodeString(args.Data)
		if err != nil {
			return nil, errors.New("input is not valid base64")
		}
		return decodedOutput(data, args.Output)
	case "hex_encode":
		data, err := decodeInput(args.Data, args.DataEncoding)
		if err != nil {
			return nil, err
		}
		return hex.EncodeToString(data), nil
	case "hex_decode":
		data, err := hex.DecodeString(args.Data)
		if err != nil {
			return nil, errors.New("input is not valid hex")
		}
		return decodedOutput(data, args.Output)
	case "random":
		if args.Length < 1 || args.Length > 1024 {
			return nil, errors.New("length must be between 1 and 1024")
		}
		buf := make([]byte, args.Length)
		if _, err := rand.Read(buf); err != nil {
			return nil, errors.New("random source unavailable")
		}
		return encodeDigest(buf, args.Output)
	case "uuid":
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, errors.New("random source unavailable")
		}
		buf[6] = buf[6]&0x0f | 0x40
		buf[8] = buf[8]&0x3f | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
	default:
		return nil, errors.New("unknown crypto operation")
	}
}

func hashFor(algorithm string) (func() hash.Hash, error) {
	switch algorithm {
	case "sha256":
		return sha256.New, nil
	case "sha1":
		return sha1.New, nil
	default:
		return nil, errors.New("algorithm must be sha256 or sha1")
	}
}

func decodeInput(data, encoding string) ([]byte, error) {
	switch encoding {
	case "", "utf8":
		return []byte(data), nil
	case "hex":
		out, err := hex.DecodeString(data)
		if err != nil {
			return nil, errors.New("input is not valid hex")
		}
		return out, nil
	case "base64":
		out, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, errors.New("input is not valid base64")
		}
		return out, nil
	default:
		return nil, errors.New("encoding must be utf8, hex or base64")
	}
}

func encodeDigest(sum []byte, output string) (string, error) {
	switch output {
	case "", "hex":
		return hex.EncodeToString(sum), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(sum), nil
	case "base64url":
		return base64.RawURLEncoding.EncodeToString(sum), nil
	default:
		return "", errors.New("output must be hex, base64 or base64url")
	}
}

func decodedOutput(data []byte, output string) (string, error) {
	switch output {
	case "", "utf8":
		if !utf8.Valid(data) {
			return "", errors.New("decoded bytes are not UTF-8; request hex output")
		}
		return string(data), nil
	case "hex":
		return hex.EncodeToString(data), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", errors.New("output must be utf8, hex or base64")
	}
}
