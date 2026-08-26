package application

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// PrepareInboundCreate resolves Controller-owned AnyTLS padding exactly once
// before the inbound row is inserted. Every creation surface calls this same
// application-layer boundary.
func (s *Service) PrepareInboundCreate(ctx context.Context, inbound *model.Inbound) error {
	if inbound == nil {
		return errors.New("inbound required")
	}
	if inbound.Protocol != model.ProtocolAnyTLS {
		if inbound.AnyTLSPadding != nil {
			return errors.New("anytls_padding is only valid for AnyTLS inbounds")
		}
		return nil
	}
	fingerprints, err := s.anyTLSPaddingFingerprints(ctx)
	if err != nil {
		return err
	}
	inbound.ConfigJSON, err = core.PrepareAnyTLSPaddingForCreate(inbound.ConfigJSON, inbound.AnyTLSPadding, fingerprints, time.Now().UTC())
	inbound.AnyTLSPadding = nil
	return err
}

func (s *Service) UpdateAnyTLSPadding(ctx context.Context, principal Principal, inboundID int64, operation core.AnyTLSPaddingOperation) (*model.Inbound, error) {
	if principal.Role != model.RoleAdmin {
		return nil, errors.New("administrator role required")
	}
	inbound, err := s.store.GetInbound(ctx, inboundID)
	if err != nil {
		return nil, err
	}
	if !principal.AllowsInt64("server_ids", inbound.ServerID) {
		return nil, sql.ErrNoRows
	}
	if inbound.Protocol != model.ProtocolAnyTLS {
		return nil, errors.New("AnyTLS padding operations require an AnyTLS inbound")
	}
	fingerprints, err := s.anyTLSPaddingFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	inbound.ConfigJSON, err = core.ApplyAnyTLSPaddingOperation(inbound.ConfigJSON, operation, fingerprints, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := s.store.UpdateInbound(ctx, inbound); err != nil {
		return nil, err
	}
	return s.store.GetInbound(ctx, inbound.ID)
}

func (s *Service) anyTLSPaddingFingerprints(ctx context.Context) (map[string]struct{}, error) {
	items, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{})
	for _, item := range items {
		if item.Protocol != model.ProtocolAnyTLS {
			continue
		}
		metadata, _, parseErr := core.AnyTLSPaddingMetadataFromJSON(item.ConfigJSON)
		if parseErr == nil && metadata != nil && metadata.Mode == core.AnyTLSPaddingModeTuned && metadata.Fingerprint != "" {
			out[metadata.Fingerprint] = struct{}{}
		}
	}
	return out, nil
}
