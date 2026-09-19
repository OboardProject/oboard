package controller

import (
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

var huaweiDNSRegions = []struct{ id, name string }{
	{"cn-north-4", "华北-北京四"},
	{"ap-southeast-3", "亚太-新加坡"},
	{"ae-ad-1", "中东-阿布扎比-OP5"},
	{"af-north-1", "非洲-开罗"},
	{"af-south-1", "非洲-约翰内斯堡"},
	{"ap-southeast-1", "中国-香港"},
	{"ap-southeast-2", "亚太-曼谷"},
	{"ap-southeast-4", "亚太-雅加达"},
	{"ap-southeast-5", "亚太-马尼拉"},
	{"cn-east-2", "华东-上海二"},
	{"cn-east-3", "华东-上海一"},
}

func signHuaweiDNSRequest(req *http.Request, payload []byte, ak, sk string, now time.Time) {
	date := now.UTC().Format("20060102T150405Z")
	req.Header.Set("X-Sdk-Date", date)
	segments := strings.Split(req.URL.Path, "/")
	for i := range segments {
		segments[i] = aliPercentEncode(segments[i])
	}
	path := strings.Join(segments, "/")
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	query := req.URL.Query()
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		sort.Strings(query[key])
		for _, value := range query[key] {
			pairs = append(pairs, aliPercentEncode(key)+"="+aliPercentEncode(value))
		}
	}
	headers := "host:" + req.URL.Host + "\nx-sdk-date:" + date + "\n"
	signedHeaders := "host;x-sdk-date"
	canonical := strings.Join([]string{req.Method, path, strings.Join(pairs, "&"), headers, signedHeaders, sha256HexBytes(payload)}, "\n")
	toSign := "SDK-HMAC-SHA256\n" + date + "\n" + sha256HexBytes([]byte(canonical))
	signature := hex.EncodeToString(hmacSHA256([]byte(sk), []byte(toSign)))
	req.Header.Set("Authorization", "SDK-HMAC-SHA256 Access="+ak+", SignedHeaders="+signedHeaders+", Signature="+signature)
}
