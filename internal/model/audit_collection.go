package model

import "time"

// AuditCollectionConfig controls evidence collection, independently of risk policy.
type AuditCollectionConfig struct {
	Mode        string            `json:"mode"`
	Revision    int64             `json:"revision"`
	Diagnostics []AuditDiagnostic `json:"diagnostics"`
}
type AuditDiagnostic struct {
	Scope string    `json:"scope"`
	ID    int64     `json:"id"`
	Until time.Time `json:"until"`
}
type AuditCollectionEffective struct {
	Mode              string     `json:"mode"`
	BaseMode          string     `json:"base_mode"`
	DiagnosticUserIDs []int64    `json:"diagnostic_user_ids"`
	DiagnosticUntil   *time.Time `json:"diagnostic_until"`
	Revision          int64      `json:"revision"`
}

const AuditDetailGlobalLimit = 100000
const AuditDetailUserLimit = 2000
const AuditDiagnosticLimit = 8

func (c AuditCollectionConfig) Effective(serverID int64, at time.Time) AuditCollectionEffective {
	e := AuditCollectionEffective{Mode: c.Mode, BaseMode: c.Mode, Revision: c.Revision, DiagnosticUserIDs: []int64{}}
	if e.Mode != "standard" {
		e.Mode = "light"
		e.BaseMode = "light"
	}
	node := false
	for _, d := range c.Diagnostics {
		if !d.Until.After(at) || (d.Scope == "node" && d.ID != serverID) {
			continue
		}
		if d.Scope == "node" {
			node = true
		} else if d.Scope == "user" {
			e.DiagnosticUserIDs = append(e.DiagnosticUserIDs, d.ID)
		} else {
			continue
		}
		// Expire the entire projection at the earliest deadline; the next heartbeat
		// restores still-active scopes without extending any individual lease.
		if e.DiagnosticUntil == nil || d.Until.Before(*e.DiagnosticUntil) {
			until := d.Until
			e.DiagnosticUntil = &until
		}
	}
	if e.DiagnosticUntil != nil {
		e.Mode = "diagnostic"
	}
	if node {
		e.DiagnosticUserIDs = []int64{}
	}
	return e
}
