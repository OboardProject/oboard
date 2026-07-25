package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

type Destination struct {
	Provider       string `json:"provider"`
	Endpoint       string `json:"endpoint"`
	Bucket         string `json:"bucket,omitempty"`
	Prefix         string `json:"prefix,omitempty"`
	Region         string `json:"region,omitempty"`
	ForcePathStyle bool   `json:"force_path_style,omitempty"`
	Enabled        bool   `json:"enabled"`
}

type RemoteSecrets struct {
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
}

type remoteResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type remoteDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

func ValidateDestination(destination Destination) error {
	return validateDestinationWithResolver(destination, net.DefaultResolver)
}

func validateDestinationWithResolver(destination Destination, resolver remoteResolver) error {
	if !destination.Enabled {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(destination.Provider))
	if provider != "s3" && provider != "webdav" {
		return errors.New("备份目标类型只能是 S3 或 WebDAV")
	}
	u, err := url.Parse(strings.TrimSpace(destination.Endpoint))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("备份目标地址无效")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("备份目标地址不能包含凭据、查询参数或片段")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "http" && !localHost(u.Hostname()) {
		return errors.New("备份目标必须使用 HTTPS，本机测试地址除外")
	}
	if scheme != "http" && scheme != "https" {
		return errors.New("备份目标必须使用 HTTPS，本机测试地址除外")
	}
	if scheme == "https" {
		if err := validatePublicRemoteURL(context.Background(), u, resolver); err != nil {
			return err
		}
	}
	if provider == "s3" {
		bucket := strings.TrimSpace(destination.Bucket)
		if bucket == "" {
			return errors.New("请填写 S3 存储桶")
		}
		if strings.ContainsAny(bucket, "/\\?#") || strings.ContainsFunc(bucket, func(r rune) bool { return r <= 0x20 }) {
			return errors.New("S3 存储桶名称无效")
		}
	}
	for _, segment := range strings.Split(strings.Trim(destination.Prefix, "/"), "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, `\`) {
			return errors.New("备份目录前缀无效")
		}
	}
	if strings.ContainsAny(destination.Prefix, "?#") {
		return errors.New("备份目录前缀无效")
	}
	return nil
}

func DestinationID(destination Destination) string {
	provider := strings.ToLower(strings.TrimSpace(destination.Provider))
	endpoint := strings.TrimRight(strings.TrimSpace(destination.Endpoint), "/")
	if u, err := url.Parse(endpoint); err == nil {
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		endpoint = strings.TrimRight(u.String(), "/")
	}
	parts := []string{provider, endpoint, strings.Trim(strings.TrimSpace(destination.Prefix), "/")}
	if provider == "s3" {
		region := strings.TrimSpace(destination.Region)
		if region == "" {
			region = "us-east-1"
		}
		parts = append(parts, strings.TrimSpace(destination.Bucket), region, fmt.Sprintf("%t", destination.ForcePathStyle))
	}
	value := strings.Join(parts, "\n")
	return sha256Hex([]byte(value))
}

func Upload(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string, file *os.File) error {
	if err := ValidateDestination(destination); err != nil {
		return err
	}
	if client == nil {
		client = newRemoteHTTPClient(2*time.Minute, destination)
	}
	if strings.TrimSpace(objectKey) == "" {
		return errors.New("备份远端文件名不能为空")
	}
	if file == nil {
		return errors.New("本地备份文件不可用")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("本地备份文件不可用")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return err
	}
	if size != info.Size() {
		return errors.New("本地备份文件读取不完整")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if strings.ToLower(destination.Provider) == "webdav" {
		if err := ensureWebDAVCollections(ctx, client, destination, secrets, objectKey); err != nil {
			return err
		}
	}
	return putObject(ctx, client, destination, secrets, objectKey, io.LimitReader(file, size), size, hex.EncodeToString(hash.Sum(nil)))
}

func putObject(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string, body io.Reader, size int64, payloadHash string) error {
	requestURL, err := destinationURL(destination, objectKey)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, requestURL.String(), body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	if strings.ToLower(destination.Provider) == "s3" {
		if err := signS3(req, payloadHash, destination, secrets); err != nil {
			return err
		}
	} else {
		req.SetBasicAuth(secrets.Username, secrets.Password)
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("第三方目标上传失败，返回 HTTP %d", response.StatusCode)
	}
	return nil
}

func Download(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string, output *os.File) (int64, error) {
	if err := ValidateDestination(destination); err != nil {
		return 0, err
	}
	if client == nil {
		client = newRemoteHTTPClient(2*time.Minute, destination)
	}
	requestURL, err := destinationURL(destination, objectKey)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return 0, err
	}
	if strings.ToLower(destination.Provider) == "s3" {
		emptyHash := sha256Hex(nil)
		if err := signS3(req, emptyHash, destination, secrets); err != nil {
			return 0, err
		}
	} else {
		req.SetBasicAuth(secrets.Username, secrets.Password)
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("第三方目标下载失败，返回 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxArchiveBytes {
		return 0, errors.New("第三方备份超过允许大小")
	}
	if output == nil {
		return 0, errors.New("本地备份目录不可用")
	}
	written, copyErr := io.Copy(output, io.LimitReader(response.Body, maxArchiveBytes+1))
	if copyErr != nil || written > maxArchiveBytes {
		if copyErr != nil {
			return 0, copyErr
		}
		return 0, errors.New("第三方备份超过允许大小")
	}
	return written, nil
}

func Delete(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string) error {
	if err := ValidateDestination(destination); err != nil {
		return err
	}
	if client == nil {
		client = newRemoteHTTPClient(2*time.Minute, destination)
	}
	requestURL, err := destinationURL(destination, objectKey)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, requestURL.String(), nil)
	if err != nil {
		return err
	}
	if strings.ToLower(destination.Provider) == "s3" {
		emptyHash := hex.EncodeToString(sha256.New().Sum(nil))
		if err := signS3(req, emptyHash, destination, secrets); err != nil {
			return err
		}
	} else {
		req.SetBasicAuth(secrets.Username, secrets.Password)
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("第三方目标删除失败，返回 HTTP %d", response.StatusCode)
	}
	return nil
}

func TestDestination(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets) error {
	if err := ValidateDestination(destination); err != nil {
		return err
	}
	if client == nil {
		client = newRemoteHTTPClient(30*time.Second, destination)
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	objectKey := ObjectKey(destination, ".oboard-write-test-"+id)
	if strings.ToLower(destination.Provider) == "webdav" {
		if err := ensureWebDAVCollections(ctx, client, destination, secrets, objectKey); err != nil {
			return err
		}
	}
	if err := putObject(ctx, client, destination, secrets, objectKey, bytes.NewReader(nil), 0, sha256Hex(nil)); err != nil {
		return fmt.Errorf("测试写入第三方目标失败：%w", err)
	}
	if err := Delete(ctx, client, destination, secrets, objectKey); err != nil {
		return fmt.Errorf("清理第三方目标测试文件失败：%w", err)
	}
	return nil
}

func ObjectKey(destination Destination, name string) string {
	prefix := strings.Trim(strings.TrimSpace(destination.Prefix), "/")
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

func destinationURL(destination Destination, objectKey string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(destination.Endpoint))
	if err != nil {
		return nil, err
	}
	objectPath := ""
	for _, segment := range strings.Split(strings.Trim(objectKey, "/"), "/") {
		if segment == "" {
			continue
		}
		objectPath += "/" + segment
	}
	base := strings.TrimRight(u.Path, "/")
	if strings.ToLower(destination.Provider) == "s3" {
		bucket := strings.TrimSpace(destination.Bucket)
		if destination.ForcePathStyle || localHost(u.Hostname()) || net.ParseIP(strings.Trim(u.Hostname(), "[]")) != nil {
			u.Path = base + "/" + bucket + objectPath
		} else {
			u.Host = bucket + "." + u.Host
			u.Path = base + objectPath
		}
	} else {
		u.Path = base + objectPath
	}
	if objectKey == "" && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

func ensureWebDAVCollections(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string) error {
	directory := path.Dir(strings.Trim(objectKey, "/"))
	if directory == "." || directory == "" {
		return nil
	}
	current := ""
	for _, segment := range strings.Split(directory, "/") {
		if segment == "" {
			continue
		}
		if current == "" {
			current = segment
		} else {
			current += "/" + segment
		}
		requestURL, err := destinationURL(destination, current)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "MKCOL", requestURL.String(), nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth(secrets.Username, secrets.Password)
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if (response.StatusCode < 200 || response.StatusCode >= 300) && response.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("创建 WebDAV 备份目录失败，返回 HTTP %d", response.StatusCode)
		}
	}
	return nil
}

func signS3(req *http.Request, payloadHash string, destination Destination, secrets RemoteSecrets) error {
	if strings.TrimSpace(secrets.AccessKey) == "" || strings.TrimSpace(secrets.SecretKey) == "" {
		return errors.New("请填写 S3 访问密钥和访问密钥密码")
	}
	region := strings.TrimSpace(destination.Region)
	if region == "" {
		region = "us-east-1"
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", amzDate)
	canonicalHeaders := "host:" + req.URL.Host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{req.Method, canonicalPath(req.URL), "", canonicalHeaders, signedHeaders, payloadHash}, "\n")
	scope := date + "/" + region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonicalRequest))}, "\n")
	dateKey := hmacSHA256([]byte("AWS4"+secrets.SecretKey), date)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+secrets.AccessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
	return nil
}

func canonicalPath(u *url.URL) string {
	if u.EscapedPath() == "" {
		return "/"
	}
	return u.EscapedPath()
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, value)
	return mac.Sum(nil)
}

func localHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newRemoteHTTPClient(timeout time.Duration, destination Destination) *http.Client {
	resolver := net.DefaultResolver
	allowLocalHTTP := strings.EqualFold(destination.EndpointScheme(), "http") && localHost(destination.EndpointHost())
	return &http.Client{
		Transport:     newRemoteTransport(resolver, &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, allowLocalHTTP),
		Timeout:       timeout,
		CheckRedirect: remoteRedirectPolicy(resolver, allowLocalHTTP),
	}
}

func (d Destination) EndpointScheme() string {
	u, err := url.Parse(strings.TrimSpace(d.Endpoint))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme)
}

func (d Destination) EndpointHost() string {
	u, err := url.Parse(strings.TrimSpace(d.Endpoint))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func remoteRedirectPolicy(resolver remoteResolver, allowLocalHTTP bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("第三方备份目标重定向次数过多")
		}
		if strings.EqualFold(req.URL.Scheme, "http") && allowLocalHTTP && localHost(req.URL.Hostname()) {
			return nil
		}
		return validatePublicRemoteURL(req.Context(), req.URL, resolver)
	}
}

func newRemoteTransport(resolver remoteResolver, dialer remoteDialer, allowLocalHTTP bool) *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("invalid backup destination address: %w", err)
			}
			ips, err := resolveRemoteIPs(ctx, host, resolver, allowLocalHTTP && localHost(host))
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errors.New("第三方备份目标无法连接")
			}
			return nil, lastErr
		},
	}
}

func validatePublicRemoteURL(ctx context.Context, u *url.URL, resolver remoteResolver) error {
	if u == nil || !strings.EqualFold(u.Scheme, "https") {
		return errors.New("备份目标必须使用 HTTPS")
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host == "" {
		return errors.New("备份目标地址无效")
	}
	if localHost(host) || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("备份目标不能指向本机、私有或链路本地地址")
	}
	_, err := resolveRemoteIPs(ctx, host, resolver, false)
	return err
}

func resolveRemoteIPs(ctx context.Context, host string, resolver remoteResolver, allowLoopback bool) ([]net.IP, error) {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if ip := net.ParseIP(host); ip != nil {
		if !allowedRemoteIP(ip, allowLoopback) {
			return nil, errors.New("备份目标不能指向本机、私有或链路本地地址")
		}
		return []net.IP{ip}, nil
	}
	if resolver == nil {
		return nil, errors.New("备份目标 DNS 解析器不可用")
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("备份目标域名无法解析: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("备份目标域名无法解析")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if !allowedRemoteIP(addr.IP, allowLoopback) {
			return nil, errors.New("备份目标不能解析到本机、私有或链路本地地址")
		}
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

func allowedRemoteIP(ip net.IP, allowLoopback bool) bool {
	if allowLoopback {
		return ip != nil && ip.IsLoopback()
	}
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 169 && ip4[1] == 254 {
			return false
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return false
		}
	}
	return true
}
