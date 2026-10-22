package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	unsignedPayload     = "UNSIGNED-PAYLOAD"
	s3MultipartPartSize = 16 << 20
)

// s3MultipartMinBytes is the object size at which S3 uploads switch to
// multipart. Tests lower it.
var s3MultipartMinBytes int64 = 64 << 20

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
		client = newRemoteHTTPClient(0, destination)
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
	size := info.Size()
	ctx, cancel := boundRemoteContext(ctx, uploadDeadline(size))
	defer cancel()
	if strings.ToLower(destination.Provider) == "webdav" {
		if err := ensureWebDAVCollections(ctx, client, destination, secrets, objectKey); err != nil {
			return err
		}
	}
	if strings.ToLower(destination.Provider) == "s3" && size >= s3MultipartMinBytes {
		return uploadS3Multipart(ctx, client, destination, secrets, objectKey, file, size)
	}
	hash := ""
	if strings.ToLower(destination.Provider) == "s3" {
		hash = unsignedPayload
	}
	return retryRemote(ctx, func() error {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		return putObject(ctx, client, destination, secrets, objectKey, &uploadProgressReader{r: io.LimitReader(file, size), total: size, ctx: ctx}, size, hash)
	})
}

func uploadDeadline(size int64) time.Duration {
	if size < 0 {
		size = 0
	}
	deadline := 2*time.Minute + time.Duration(size/(32<<20))*time.Minute
	if deadline < 2*time.Minute {
		return 2 * time.Minute
	}
	if deadline > 6*time.Hour {
		return 6 * time.Hour
	}
	return deadline
}

func boundRemoteContext(ctx context.Context, fallback time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, fallback)
}

type uploadProgressReader struct {
	r     io.Reader
	total int64
	read  int64
	ctx   context.Context
}

func (r *uploadProgressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.read += int64(n)
		percent := 0.0
		if r.total > 0 {
			percent = float64(r.read) / float64(r.total) * 100
		}
		ReportProgress(r.ctx, "upload", percent)
	}
	return n, err
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

func retryRemote(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		err = fn()
		if err == nil || !remoteRetryable(err) {
			return err
		}
	}
	return err
}

func remoteRetryable(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	message := err.Error()
	for _, code := range []string{"HTTP 429", "HTTP 500", "HTTP 502", "HTTP 503", "HTTP 504"} {
		if strings.Contains(message, code) {
			return true
		}
	}
	return false
}

type s3CompletedPart struct {
	Number int
	ETag   string
}

func uploadS3Multipart(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string, file *os.File, size int64) error {
	uploadID, err := s3InitiateMultipart(ctx, client, destination, secrets, objectKey)
	if err != nil {
		return err
	}
	var parts []s3CompletedPart
	err = func() error {
		buf := make([]byte, s3MultipartPartSize)
		var sent int64
		for number := 1; ; number++ {
			n, readErr := io.ReadFull(file, buf)
			if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
				return readErr
			}
			if n == 0 {
				break
			}
			etag, partErr := s3UploadPart(ctx, client, destination, secrets, objectKey, uploadID, number, buf[:n])
			if partErr != nil {
				return partErr
			}
			parts = append(parts, s3CompletedPart{Number: number, ETag: etag})
			sent += int64(n)
			percent := 0.0
			if size > 0 {
				percent = float64(sent) / float64(size) * 100
			}
			ReportProgress(ctx, "upload", percent)
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				break
			}
		}
		if len(parts) == 0 {
			return errors.New("备份文件为空，无法分段上传")
		}
		return s3CompleteMultipart(ctx, client, destination, secrets, objectKey, uploadID, parts)
	}()
	if err != nil {
		_ = s3AbortMultipart(ctx, client, destination, secrets, objectKey, uploadID)
		return err
	}
	return nil
}

func s3InitiateMultipart(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string) (string, error) {
	var uploadID string
	err := retryRemote(ctx, func() error {
		req, err := s3Request(ctx, http.MethodPost, destination, secrets, objectKey, url.Values{"uploads": {""}}, nil, 0)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("第三方目标上传失败，返回 HTTP %d", response.StatusCode)
		}
		var result struct {
			UploadID string `xml:"UploadId"`
		}
		if err := xml.Unmarshal(body, &result); err != nil || strings.TrimSpace(result.UploadID) == "" {
			return errors.New("第三方目标没有返回分段上传编号")
		}
		uploadID = result.UploadID
		return nil
	})
	return uploadID, err
}

func s3UploadPart(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey, uploadID string, number int, body []byte) (string, error) {
	var etag string
	err := retryRemote(ctx, func() error {
		query := url.Values{"partNumber": {strconv.Itoa(number)}, "uploadId": {uploadID}}
		req, err := s3Request(ctx, http.MethodPut, destination, secrets, objectKey, query, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("第三方目标上传失败，返回 HTTP %d", response.StatusCode)
		}
		etag = response.Header.Get("ETag")
		if etag == "" {
			return errors.New("第三方目标没有返回分段校验值")
		}
		return nil
	})
	return etag, err
}

func s3CompleteMultipart(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey, uploadID string, parts []s3CompletedPart) error {
	var body bytes.Buffer
	body.WriteString("<CompleteMultipartUpload>")
	for _, part := range parts {
		fmt.Fprintf(&body, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", part.Number, xmlEscape(part.ETag))
	}
	body.WriteString("</CompleteMultipartUpload>")
	payload := body.Bytes()
	return retryRemote(ctx, func() error {
		req, err := s3Request(ctx, http.MethodPost, destination, secrets, objectKey, url.Values{"uploadId": {uploadID}}, bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/xml")
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("第三方目标上传失败，返回 HTTP %d", response.StatusCode)
		}
		return nil
	})
}

func s3AbortMultipart(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey, uploadID string) error {
	req, err := s3Request(ctx, http.MethodDelete, destination, secrets, objectKey, url.Values{"uploadId": {uploadID}}, nil, 0)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("第三方目标上传失败，返回 HTTP %d", response.StatusCode)
	}
	return nil
}

func s3Request(ctx context.Context, method string, destination Destination, secrets RemoteSecrets, objectKey string, query url.Values, body io.Reader, size int64) (*http.Request, error) {
	requestURL, err := destinationURL(destination, objectKey)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		requestURL.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	if err := signS3(req, unsignedPayload, destination, secrets); err != nil {
		return nil, err
	}
	return req, nil
}

func xmlEscape(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
}

func Download(ctx context.Context, client *http.Client, destination Destination, secrets RemoteSecrets, objectKey string, output *os.File) (int64, error) {
	if err := ValidateDestination(destination); err != nil {
		return 0, err
	}
	ctx, cancel := boundRemoteContext(ctx, 30*time.Minute)
	defer cancel()
	if client == nil {
		client = newRemoteHTTPClient(0, destination)
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
	canonicalRequest := strings.Join([]string{req.Method, canonicalPath(req.URL), canonicalQuery(req.URL), canonicalHeaders, signedHeaders, payloadHash}, "\n")
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

func canonicalQuery(u *url.URL) string {
	if u == nil || u.RawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return u.RawQuery
	}
	return values.Encode()
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
	transport := newRemoteTransport(resolver, &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, allowLocalHTTP)
	transport.ResponseHeaderTimeout = 2 * time.Minute
	return &http.Client{
		Transport:     transport,
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
		DisableKeepAlives:   false,
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
