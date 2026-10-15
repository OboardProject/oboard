package model

import "time"

// ProbeDDNSRecord is a server-scoped dynamic DNS record owned by probe
// monitoring. Inbounds reference it when their client host should be this
// name. The record, not the inbound, updates the provider.
type ProbeDDNSRecord struct {
	ID               int64      `json:"id"`
	ServerID         int64      `json:"server_id"`
	Name             string     `json:"name"`
	DNSCredentialID  *int64     `json:"dns_credential_id,omitempty"`
	Domain           string     `json:"domain"`
	RecordTypes      string     `json:"record_types"`
	ProxyEnabled     bool       `json:"proxy_enabled"`
	IntervalSeconds  int        `json:"interval_seconds"`
	Enabled          bool       `json:"enabled"`
	InterfaceName    string     `json:"interface_name,omitempty"`
	InterfaceAddress string     `json:"interface_address,omitempty"`
	AddressFamily    string     `json:"address_family,omitempty"`
	ExcludePrivate   bool       `json:"exclude_private"`
	Revision         int64      `json:"revision"`
	Status           string     `json:"status,omitempty"`
	Error            string     `json:"error,omitempty"`
	CurrentAddress   string     `json:"current_address,omitempty"`
	LastSyncedAt     *time.Time `json:"last_synced_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// InterfaceBound reports whether updates must use a reported NIC address.
func (v ProbeDDNSRecord) InterfaceBound() bool {
	return v.InterfaceName != "" || v.InterfaceAddress != ""
}
