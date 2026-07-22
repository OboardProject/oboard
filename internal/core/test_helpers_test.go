package core

import "github.com/OboardProject/oboard/internal/model"

func GenerateServerConfig(server model.Server, inbounds []model.Inbound, outbounds []model.Outbound, dnsState *DNSConfigState, users []model.User) (string, error) {
	return GenerateServerConfigWithOptions(server, inbounds, outbounds, dnsState, users, ConfigOptions{})
}

func GenerateSubscription(user model.User, servers []model.Server, inbounds []model.Inbound) (string, error) {
	return GenerateSubscriptionWithOptions(user, servers, inbounds, SubscriptionOptions{Format: model.SubscriptionFormatSingBox})
}
