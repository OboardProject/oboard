package store

import (
	"context"

	"github.com/OboardProject/oboard/internal/model"
)

// ProxyTopologyData is the smallest read model needed to validate
// and name proxy paths. It intentionally excludes users, plans, DNS policy,
// and other deployment-only state from latency-sensitive topology writes.
type ProxyTopologyData struct {
	Servers                  []model.Server
	Inbounds                 []model.Inbound
	ExternalOutbounds        []model.ExternalOutbound
	ProxyPaths               []model.ProxyPath
	ProxyPathSteps           []model.ProxyPathStep
	ProxyPathEgressResults   []model.ProxyPathEgressResult
	ProxyPathPortAllocations []model.ProxyPathPortAllocation
}

func (s *Store) ResetProxyPathNameTemplate(ctx context.Context, pathID int64) error {
	_, err := s.db.ExecContext(ctx, `update proxy_paths set name_mode='auto',name_template_json='[]',updated_at=? where id=?`, now(), pathID)
	return err
}

func (s *Store) ProxyTopologyData(ctx context.Context) (ProxyTopologyData, error) {
	servers, err := s.ListServers(ctx)
	if err != nil {
		return ProxyTopologyData{}, err
	}
	inbounds, err := s.ListInbounds(ctx)
	if err != nil {
		return ProxyTopologyData{}, err
	}
	externals, err := s.ListExternalOutbounds(ctx)
	if err != nil {
		return ProxyTopologyData{}, err
	}
	paths, err := s.ListProxyPaths(ctx)
	if err != nil {
		return ProxyTopologyData{}, err
	}
	steps, err := s.ListProxyPathSteps(ctx)
	if err != nil {
		return ProxyTopologyData{}, err
	}
	egress, err := s.ListProxyPathEgressResults(ctx)
	if err != nil {
		return ProxyTopologyData{}, err
	}
	allocations, err := s.ListProxyPathPortAllocations(ctx)
	if err != nil {
		return ProxyTopologyData{}, err
	}
	return ProxyTopologyData{
		Servers:                  servers,
		Inbounds:                 inbounds,
		ExternalOutbounds:        externals,
		ProxyPaths:               paths,
		ProxyPathSteps:           steps,
		ProxyPathEgressResults:   egress,
		ProxyPathPortAllocations: allocations,
	}, nil
}
