package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const aiProviderSelect = `select id,name,provider_kind,default_model,routing_strategy,base_url,model,api_format,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,capability_json,last_used_at,created_at,updated_at from ai_providers`

func normalizeAIProvider(item *model.AIProvider) {
	if item.ProviderKind == "" {
		item.ProviderKind = "openai"
	}
	if item.DefaultModel == "" {
		item.DefaultModel = item.Model
	}
	if item.Model == "" {
		item.Model = item.DefaultModel
	}
	if item.RoutingStrategy == "" {
		item.RoutingStrategy = "ordered_failover"
	}
	if item.APIFormat == "" {
		item.APIFormat = "chat_completions"
	}
}

func (s *Store) CreateAIProvider(ctx context.Context, item *model.AIProvider) error {
	if item == nil {
		return errors.New("AI provider is required")
	}
	normalizeAIProvider(item)
	ts := now()
	capabilityJSON := marshalCapability(item.Capability)
	_, err := s.db.ExecContext(ctx, `insert into ai_providers(id,name,provider_kind,default_model,routing_strategy,base_url,model,api_format,credential_encrypted,enabled,allow_raw_audit,daily_token_limit,capability_json,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Name, item.ProviderKind, item.DefaultModel, item.RoutingStrategy, item.BaseURL, item.Model, item.APIFormat, item.CredentialEncrypted, boolInt(item.Enabled), boolInt(item.AllowRawAudit), item.DailyTokenLimit, capabilityJSON, ts, ts)
	if err == nil {
		item.HasCredential = item.CredentialEncrypted != ""
		item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	}
	return err
}

func (s *Store) ListAIProviders(ctx context.Context) ([]model.AIProvider, error) {
	rows, err := s.db.QueryContext(ctx, aiProviderSelect+` order by created_at`)
	if err != nil {
		return nil, err
	}
	out := []model.AIProvider{}
	for rows.Next() {
		item, err := scanAIProvider(rows)
		if err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		out = append(out, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range out {
		item := &out[index]
		item.Endpoints, err = s.ListAIProviderEndpoints(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		s.hydrateProviderCapabilities(ctx, item)
		item.HasCredential = providerHasCredential(item)
		item.CredentialEncrypted = ""
	}
	return out, nil
}

func (s *Store) GetAIProvider(ctx context.Context, id string) (*model.AIProvider, error) {
	item, err := scanAIProvider(s.db.QueryRowContext(ctx, aiProviderSelect+` where id=?`, id))
	if err != nil {
		return nil, err
	}
	item.Endpoints, err = s.ListAIProviderEndpoints(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	s.hydrateProviderCapabilities(ctx, item)
	item.HasCredential = providerHasCredential(item)
	return item, nil
}

func (s *Store) hydrateProviderCapabilities(ctx context.Context, item *model.AIProvider) {
	for index := range item.Endpoints {
		endpoint := &item.Endpoints[index]
		modelID := item.DefaultModel
		if endpoint.ModelOverride != "" {
			modelID = endpoint.ModelOverride
		}
		digest := aiprovider.ConfigDigest(aiprovider.RuntimeEndpoint{BaseURL: endpoint.BaseURL, APIStyle: aiprovider.APIStyle(endpoint.APIStyle), AuthMode: endpoint.AuthMode, AnthropicVersion: endpoint.AnthropicVersion, Headers: endpointHeaders(endpoint.HeadersJSON), ModelsPath: endpoint.ModelsPath, GeneratePath: endpoint.GeneratePath}, modelID)
		if capability, err := s.GetAIProviderEndpointCapability(ctx, endpoint.ID, modelID, digest); err == nil {
			endpoint.Capability = capability
		}
	}
}

func endpointHeaders(raw string) map[string]string {
	headers := map[string]string{}
	_ = json.Unmarshal([]byte(raw), &headers)
	return headers
}

func providerHasCredential(item *model.AIProvider) bool {
	if item.CredentialEncrypted != "" {
		return true
	}
	for _, endpoint := range item.Endpoints {
		if endpoint.AuthMode == aiprovider.AuthModeNone || endpoint.HasCredential {
			return true
		}
	}
	return false
}

func (s *Store) UpdateAIProvider(ctx context.Context, item *model.AIProvider) error {
	if item == nil {
		return errors.New("AI provider is required")
	}
	normalizeAIProvider(item)
	if item.Capability == nil {
		if existing, err := s.GetAIProvider(ctx, item.ID); err == nil {
			item.Capability = existing.Capability
			if item.CredentialEncrypted == "" {
				item.CredentialEncrypted = existing.CredentialEncrypted
			}
		}
	}
	result, err := s.db.ExecContext(ctx, `update ai_providers set name=?,provider_kind=?,default_model=?,routing_strategy=?,base_url=?,model=?,api_format=?,credential_encrypted=?,enabled=?,allow_raw_audit=?,daily_token_limit=?,capability_json=?,updated_at=? where id=?`, item.Name, item.ProviderKind, item.DefaultModel, item.RoutingStrategy, item.BaseURL, item.Model, item.APIFormat, item.CredentialEncrypted, boolInt(item.Enabled), boolInt(item.AllowRawAudit), item.DailyTokenLimit, marshalCapability(item.Capability), now(), item.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteAIProvider(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `delete from ai_providers where id=? and not exists(select 1 from ai_audit_reviews where provider_id=?)`, id, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateAIProviderEndpoint(ctx context.Context, item *model.AIProviderEndpoint) error {
	if item == nil {
		return errors.New("AI provider endpoint is required")
	}
	normalizeAIProviderEndpoint(item)
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into ai_provider_endpoints(id,provider_id,name,base_url,api_style,auth_mode,credential_encrypted,anthropic_version,headers_json,models_path,generate_path,model_override,priority,enabled,timeout_ms,max_retries,allow_private_network,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.ProviderID, item.Name, item.BaseURL, item.APIStyle, item.AuthMode, item.CredentialEncrypted, item.AnthropicVersion, item.HeadersJSON, item.ModelsPath, item.GeneratePath, item.ModelOverride, item.Priority, boolInt(item.Enabled), item.TimeoutMS, item.MaxRetries, boolInt(item.AllowPrivateNetwork), ts, ts)
	if err == nil {
		item.HasCredential = item.CredentialEncrypted != ""
		item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	}
	return err
}

func normalizeAIProviderEndpoint(item *model.AIProviderEndpoint) {
	if strings.TrimSpace(item.HeadersJSON) == "" {
		item.HeadersJSON = "{}"
	}
	if item.Priority <= 0 {
		item.Priority = 100
	}
	if item.TimeoutMS <= 0 {
		item.TimeoutMS = 60000
	}
	if item.MaxRetries < 0 {
		item.MaxRetries = 0
	}
}

func (s *Store) ListAIProviderEndpoints(ctx context.Context, providerID string) ([]model.AIProviderEndpoint, error) {
	rows, err := s.db.QueryContext(ctx, `select id,provider_id,name,base_url,api_style,auth_mode,credential_encrypted,anthropic_version,headers_json,models_path,generate_path,model_override,priority,enabled,timeout_ms,max_retries,allow_private_network,created_at,updated_at from ai_provider_endpoints where provider_id=? order by priority,id`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AIProviderEndpoint{}
	for rows.Next() {
		item, err := scanAIProviderEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) GetAIProviderEndpoint(ctx context.Context, providerID, endpointID string) (*model.AIProviderEndpoint, error) {
	return scanAIProviderEndpoint(s.db.QueryRowContext(ctx, `select id,provider_id,name,base_url,api_style,auth_mode,credential_encrypted,anthropic_version,headers_json,models_path,generate_path,model_override,priority,enabled,timeout_ms,max_retries,allow_private_network,created_at,updated_at from ai_provider_endpoints where provider_id=? and id=?`, providerID, endpointID))
}

func (s *Store) UpdateAIProviderEndpoint(ctx context.Context, item *model.AIProviderEndpoint) error {
	if item == nil {
		return errors.New("AI provider endpoint is required")
	}
	normalizeAIProviderEndpoint(item)
	result, err := s.db.ExecContext(ctx, `update ai_provider_endpoints set name=?,base_url=?,api_style=?,auth_mode=?,credential_encrypted=?,anthropic_version=?,headers_json=?,models_path=?,generate_path=?,model_override=?,priority=?,enabled=?,timeout_ms=?,max_retries=?,allow_private_network=?,updated_at=? where provider_id=? and id=?`, item.Name, item.BaseURL, item.APIStyle, item.AuthMode, item.CredentialEncrypted, item.AnthropicVersion, item.HeadersJSON, item.ModelsPath, item.GeneratePath, item.ModelOverride, item.Priority, boolInt(item.Enabled), item.TimeoutMS, item.MaxRetries, boolInt(item.AllowPrivateNetwork), now(), item.ProviderID, item.ID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteAIProviderEndpoint(ctx context.Context, providerID, endpointID string) error {
	result, err := s.db.ExecContext(ctx, `delete from ai_provider_endpoints where provider_id=? and id=?`, providerID, endpointID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpsertAIProviderEndpointCapability(ctx context.Context, capability *model.AIProviderCapability) error {
	if capability == nil || capability.EndpointID == "" || capability.Model == "" || capability.ConfigDigest == "" {
		return errors.New("complete endpoint capability is required")
	}
	_, err := s.db.ExecContext(ctx, `insert into ai_provider_endpoint_capabilities(provider_id,endpoint_id,model,config_digest,capability_json,tested_at) values(?,?,?,?,?,?) on conflict(endpoint_id,model,config_digest) do update set provider_id=excluded.provider_id,capability_json=excluded.capability_json,tested_at=excluded.tested_at`, capability.ProviderID, capability.EndpointID, capability.Model, capability.ConfigDigest, marshalCapability(capability), capability.TestedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetAIProviderEndpointCapability(ctx context.Context, endpointID, modelID, digest string) (*model.AIProviderCapability, error) {
	var encoded string
	err := s.db.QueryRowContext(ctx, `select capability_json from ai_provider_endpoint_capabilities where endpoint_id=? and model=? and config_digest=?`, endpointID, modelID, digest).Scan(&encoded)
	if err != nil {
		return nil, err
	}
	var capability model.AIProviderCapability
	if err := json.Unmarshal([]byte(encoded), &capability); err != nil {
		return nil, err
	}
	return &capability, nil
}

// MigrateAIProvidersV2 runs after OBOARD_SESSION_SECRET is available. It is
// transactional because legacy and endpoint credentials use different AAD.
func (s *Store) MigrateAIProvidersV2(ctx context.Context, masterSecret string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id,name,base_url,model,api_format,credential_encrypted,created_at,updated_at from ai_providers where not exists(select 1 from ai_provider_endpoints where provider_id=ai_providers.id) and (base_url<>'' or credential_encrypted<>'')`)
	if err != nil {
		return err
	}
	type legacyProvider struct{ id, name, baseURL, model, format, credential, created, updated string }
	legacy := []legacyProvider{}
	for rows.Next() {
		var item legacyProvider
		if err := rows.Scan(&item.id, &item.name, &item.baseURL, &item.model, &item.format, &item.credential, &item.created, &item.updated); err != nil {
			return errors.Join(err, rows.Close())
		}
		legacy = append(legacy, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range legacy {
		endpointID := legacyEndpointID(item.id)
		credential := ""
		if item.credential != "" {
			plain, decryptErr := security.DecryptSecret(masterSecret, "ai-provider-credential:"+item.id, item.credential)
			if decryptErr != nil {
				return decryptErr
			}
			credential, err = security.EncryptSecret(masterSecret, "ai-provider-endpoint-credential:"+endpointID, plain)
			if err != nil {
				return err
			}
		}
		style := "openai_chat_completions"
		if item.format == "responses" || item.format == "openai_responses" {
			style = "openai_responses"
		}
		allowPrivate := false
		if _, publicErr := aiprovider.NormalizeEndpointBaseURL(item.baseURL, false); publicErr != nil {
			_, privateErr := aiprovider.NormalizeEndpointBaseURL(item.baseURL, true)
			allowPrivate = privateErr == nil
		}
		if _, err = tx.ExecContext(ctx, `insert into ai_provider_endpoints(id,provider_id,name,base_url,api_style,auth_mode,credential_encrypted,headers_json,priority,enabled,timeout_ms,max_retries,allow_private_network,created_at,updated_at) values(?,?,?,?,?,'bearer',?,'{}',100,1,60000,2,?,?,?)`, endpointID, item.id, "Primary", item.baseURL, style, credential, boolInt(allowPrivate), item.created, item.updated); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `update ai_providers set provider_kind='openai',default_model=?,routing_strategy='ordered_failover',base_url='',credential_encrypted='',capability_json='',updated_at=? where id=?`, item.model, item.updated, item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func legacyEndpointID(providerID string) string {
	sum := sha256.Sum256([]byte("ai-provider-endpoint-v2\x00" + providerID))
	return "aipe_" + hex.EncodeToString(sum[:12])
}

func scanAIProvider(scanner interface{ Scan(...any) error }) (*model.AIProvider, error) {
	var item model.AIProvider
	var enabled, raw int
	var capabilityJSON string
	var last sql.NullString
	var created, updated string
	if err := scanner.Scan(&item.ID, &item.Name, &item.ProviderKind, &item.DefaultModel, &item.RoutingStrategy, &item.BaseURL, &item.Model, &item.APIFormat, &item.CredentialEncrypted, &enabled, &raw, &item.DailyTokenLimit, &capabilityJSON, &last, &created, &updated); err != nil {
		return nil, err
	}
	item.Enabled, item.AllowRawAudit, item.HasCredential = enabled != 0, raw != 0, item.CredentialEncrypted != ""
	if strings.TrimSpace(capabilityJSON) != "" {
		var capability model.AIProviderCapability
		if json.Unmarshal([]byte(capabilityJSON), &capability) == nil {
			item.Capability = &capability
		}
	}
	normalizeAIProvider(&item)
	item.LastUsedAt, item.CreatedAt, item.UpdatedAt = nullableTime(last), parseTime(created), parseTime(updated)
	return &item, nil
}

func scanAIProviderEndpoint(scanner interface{ Scan(...any) error }) (*model.AIProviderEndpoint, error) {
	var item model.AIProviderEndpoint
	var enabled, private int
	var created, updated string
	if err := scanner.Scan(&item.ID, &item.ProviderID, &item.Name, &item.BaseURL, &item.APIStyle, &item.AuthMode, &item.CredentialEncrypted, &item.AnthropicVersion, &item.HeadersJSON, &item.ModelsPath, &item.GeneratePath, &item.ModelOverride, &item.Priority, &enabled, &item.TimeoutMS, &item.MaxRetries, &private, &created, &updated); err != nil {
		return nil, err
	}
	item.Enabled, item.AllowPrivateNetwork = enabled != 0, private != 0
	item.HasCredential = item.CredentialEncrypted != ""
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

func marshalCapability(capability *model.AIProviderCapability) string {
	if capability == nil {
		return ""
	}
	encoded, err := json.Marshal(capability)
	if err != nil {
		return ""
	}
	return string(encoded)
}
