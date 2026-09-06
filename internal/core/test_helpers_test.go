package core

import (
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
)

func GenerateServerConfig(server model.Server, inbounds []model.Inbound, outbounds []model.Outbound, dnsState *DNSConfigState, users []model.User) (string, error) {
	return generateFixtureConfig(server, inbounds, outbounds, dnsState, users, ConfigOptions{})
}

func GenerateSubscription(user model.User, servers []model.Server, inbounds []model.Inbound) (string, error) {
	return generateFixtureSubscription(user, servers, inbounds, SubscriptionOptions{Format: model.SubscriptionFormatSingBox})
}

// fixtureCredentials models already persisted credentials for rendering tests.
// Authorization decisions still use the bindings passed to the real renderer.
func fixtureCredentials(users []model.User, inbounds []model.Inbound, paths []model.ProxyPath) []model.User {
	out := append([]model.User(nil), users...)
	for i, user := range out {
		if user.ProxyCredentials != nil {
			continue
		}
		for _, in := range inbounds {
			branchIDs := []int64{0}
			if in.Protocol == model.ProtocolSSH {
				branchIDs = []int64{SSHDirectBranchPathID(in.ID)}
			}
			for _, path := range paths {
				if path.InboundID == in.ID {
					branchIDs = append(branchIDs, path.ID)
				}
			}
			for _, pathID := range branchIDs {
				name, password, uuid := user.Username, user.ProxyPassword, user.ProxyUUID
				if name == "" {
					name = fmt.Sprintf("fixture-user-%d", user.ID)
				}
				if uuid == "" {
					uuid = "11111111-1111-4111-8111-111111111111"
				}
				if password == "" {
					password = "fixture-password"
				}
				if in.Protocol == model.ProtocolSnell && len(password) < 12 {
					password = deterministicSecret(password)
				}
				if pathID > 0 {
					name = fmt.Sprintf("%s__oboard_path_%d", name, pathID)
					password = fmt.Sprintf("fixture-password-%d-%d", user.ID, pathID)
					uuid = deterministicUUID(password)
				}
				if in.Protocol == model.ProtocolMieru {
					name = fmt.Sprintf("oboard-u%d", user.ID)
					if pathID > 0 {
						name += fmt.Sprintf("-p%d", pathID)
					}
				}
				out[i].ProxyCredentials = append(out[i].ProxyCredentials, model.ProxyCredential{ID: fmt.Sprintf("fixture-%d-%d-%d", user.ID, in.ID, pathID), UserID: user.ID, InboundID: in.ID, PathID: pathID, DeviceIDHash: user.DeviceIDHash, CredentialEpoch: user.CredentialEpoch, Protocol: in.Protocol, Status: "active", Username: name, Password: password, UUID: uuid})
			}
		}
	}
	return out
}

func generateFixtureConfig(server model.Server, inbounds []model.Inbound, outbounds []model.Outbound, dns *DNSConfigState, users []model.User, opts ConfigOptions) (string, error) {
	all := append(append([]model.Inbound(nil), inbounds...), opts.Inbounds...)
	return GenerateServerConfigWithOptions(server, inbounds, outbounds, dns, fixtureCredentials(users, all, opts.ProxyPaths), opts)
}
func generateFixtureSubscription(user model.User, servers []model.Server, inbounds []model.Inbound, opts SubscriptionOptions) (string, error) {
	return GenerateSubscriptionWithOptions(fixtureCredentials([]model.User{user}, inbounds, opts.ProxyPaths)[0], servers, inbounds, opts)
}

func buildFixtureSubscriptionNodes(user model.User, servers []model.Server, inbounds []model.Inbound, opts SubscriptionOptions) ([]SubscriptionNode, error) {
	return BuildSubscriptionNodes(fixtureCredentials([]model.User{user}, inbounds, opts.ProxyPaths)[0], servers, inbounds, opts)
}
