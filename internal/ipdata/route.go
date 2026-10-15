package ipdata

import (
	"errors"
	"net/netip"

	"github.com/OboardProject/oboard/internal/model"
)

// ASNPath requires complete, single-ASN observations for every TTL. Missing
// hops and unresolved multipath are unknown, never evidence of a route change.
func ASNPath(trace model.NetworkTraceResult, lookup func(string) (string, error)) ([]string, error) {
	if !trace.Reached || trace.Truncated || len(trace.Hops) == 0 || len(trace.Hops) > model.TraceMaxHops {
		return nil, errors.New("路由未完整到达目标，本次不判定变化")
	}
	path := []string{}
	for i, hop := range trace.Hops {
		if hop.Hop != i+1 || len(hop.Addresses) == 0 || len(hop.Addresses) > model.TraceMaxQueriesPerHop {
			return nil, errors.New("路由存在未回应跳，本次不判定变化")
		}
		asn := ""
		private := false
		for _, address := range hop.Addresses {
			ip, err := netip.ParseAddr(address)
			if err != nil {
				return nil, errors.New("路由地址无效")
			}
			if ip.IsPrivate() {
				private = true
				continue
			}
			if !PublicIP(address) {
				return nil, errors.New("路由含无法归属的地址")
			}
			next, err := lookup(address)
			if err != nil || !asnPattern.MatchString(next) {
				return nil, errors.New("ASN 查询不完整，本次不判定变化")
			}
			if asn != "" && asn != next {
				return nil, errors.New("同一跳存在多个 ASN，本次不判定变化")
			}
			asn = next
		}
		if private && asn != "" {
			return nil, errors.New("同一跳存在混合路由，本次不判定变化")
		}
		if asn == "" {
			continue
		}
		if len(path) == 0 || path[len(path)-1] != asn {
			path = append(path, asn)
		}
	}
	if len(path) == 0 {
		return nil, errors.New("没有可比较的 ASN 路径")
	}
	return path, nil
}
