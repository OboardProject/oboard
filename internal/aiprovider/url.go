package aiprovider

import (
	"errors"
	"net/netip"
	"net/url"
	pathpkg "path"
	"strings"
)

func NormalizeEndpointBaseURL(raw string, allowPrivate bool) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("endpoint base URL must be an HTTP or HTTPS API version root")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", errors.New("endpoint base URL host is required")
	}
	privateTarget := strings.EqualFold(host, "localhost")
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		ip = ip.Unmap()
		if blockedProviderIP(ip, true) {
			return "", errors.New("endpoint base URL uses a prohibited address")
		}
		privateTarget = ip.IsPrivate() || ip.IsLoopback()
	}
	if privateTarget && !allowPrivate {
		return "", errors.New("private or loopback endpoint requires explicit private network access")
	}
	if parsed.Scheme == "http" && !privateTarget {
		return "", errors.New("public provider endpoints must use HTTPS")
	}
	return value, nil
}

func ResolveEndpointURL(baseURL, operationPath string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid endpoint base URL")
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, errors.New("endpoint base URL must use HTTP or HTTPS")
	}
	override, err := url.Parse(strings.TrimSpace(operationPath))
	if err != nil || override.IsAbs() || override.Host != "" || override.User != nil || override.RawQuery != "" || override.Fragment != "" {
		return nil, errors.New("invalid endpoint operation path")
	}
	cleanOperation := pathpkg.Clean("/" + strings.TrimPrefix(override.Path, "/"))
	if cleanOperation == "/." || strings.Contains(cleanOperation, "..") {
		return nil, errors.New("invalid endpoint operation path")
	}
	result := *base
	result.Path = pathpkg.Join(strings.TrimSuffix(base.Path, "/"), cleanOperation)
	result.RawPath = ""
	return &result, nil
}

func DefaultPaths(style APIStyle) (models, generate string) {
	switch style {
	case APIStyleOpenAIResponses:
		return "/models", "/responses"
	case APIStyleOpenAIChatCompletions:
		return "/models", "/chat/completions"
	case APIStyleAnthropicMessages:
		return "/models", "/messages"
	default:
		return "", ""
	}
}
