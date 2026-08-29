package core

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	DefaultProxyPathChainMethod            = "2022-blake3-aes-128-gcm"
	DefaultProxyPathRealityHandshakeServer = "cdn.icloud-content.com"
	DefaultProxyPathRealityHandshakePort   = 443
)

var proxyPathChainMethods = map[string]int{
	"2022-blake3-aes-128-gcm":       1,
	"2022-blake3-aes-256-gcm":       2,
	"2022-blake3-chacha20-poly1305": 3,
}

// PortRequirement describes one generated listener Controller needs a port
// for. The pool, listen address and network classify the listener; the ledger
// persists them with the port so allocation and final deployment share the same
// address/port/protocol model and so a policy change can decide which owners
// must migrate without guessing from the port number.
type PortRequirement struct {
	Kind           string
	ScopeKey       string
	ServerID       int64
	Pool           string
	ListenIP       string
	Network        model.ForwardProtocol
	PolicyRevision int64
	Allocate       func() int
	// AllocateOrdinal, when set, allocates the port for one ordinal of a
	// multiport owner (Mieru). It is used for atomic group allocation: every
	// ordinal is probed before any row is recorded, so a group either commits
	// whole or leaves the ledger unchanged.
	AllocateOrdinal func(ordinal int) int
}

// Migration phase views for generation-aware projections. Ordinary deployments
// read only the active generation; a migration renders more generations
// depending on its phase.
const (
	PortMigrationPhasePrepare = "prepare"
	PortMigrationPhaseSwitch  = "switch"
	PortMigrationPhaseRetire  = "retire"
)

// proxyPathPortGeneration is one generation of one logical owner. Steady state
// is a single active generation; a migration adds a preparing generation and
// keeps the old one around as retiring until the migration flow deletes it.
type proxyPathPortGeneration struct {
	generation     int
	state          string
	policyRevision int64
	ports          map[int]model.ProxyPathPortAllocation
}

type proxyPathPortOwner struct {
	key         proxyPathPortKey
	generations []*proxyPathPortGeneration
}

// ProxyPathPortLedger holds the persisted generated-listener ports and records
// the ones a projection had to allocate. A projection prefers a stored port so
// that an unrelated change — a new inbound, a disabled branch, another path
// claiming a nearby port — cannot move a listener that is already deployed.
// The port is owned by the logical (kind, scope_key, server_id) owner, not the
// other way around; a policy change may assign the owner a new port without
// rotating any identity that depends on the owner.
//
// Generations are selected by explicit state, never by "highest generation":
// ordinary resolve() reads the active generation only, so a preparing or
// retiring row can never leak into a normal deployment.
//
// The ledger is read-only with respect to the database. Controller persists
// Pending() after a projection succeeds, so a rejected deployment leaves no
// half-claimed ports behind.
type ProxyPathPortLedger struct {
	owners       map[proxyPathPortKey]*proxyPathPortOwner
	pending      []model.ProxyPathPortAllocation
	pendingByKey map[proxyPathPortKey]bool
	used         map[proxyPathPortKey]bool
	removed      map[int64]bool
	complete     bool
}

type proxyPathPortKey struct {
	Kind     string
	ScopeKey string
	ServerID int64
}

// NewProxyPathPortLedger builds a ledger from persisted allocations. A nil or
// empty slice yields a ledger that allocates everything fresh, which is also the
// behavior pure-Core callers and fixtures get. Legacy rows without lifecycle
// metadata are normalized to generation 1 active ordinal 0.
func NewProxyPathPortLedger(allocations []model.ProxyPathPortAllocation) *ProxyPathPortLedger {
	ledger := &ProxyPathPortLedger{
		owners:       map[proxyPathPortKey]*proxyPathPortOwner{},
		pendingByKey: map[proxyPathPortKey]bool{},
		used:         map[proxyPathPortKey]bool{},
		removed:      map[int64]bool{},
	}
	for _, item := range allocations {
		if item.Port <= 0 {
			continue
		}
		owner := ledger.ownerFor(item.Kind, item.ScopeKey, item.ServerID)
		generation := item.Generation
		if generation <= 0 {
			generation = 1
		}
		gen := owner.generationFor(generation)
		gen.state = normalizeAllocationState(item.State)
		if item.PolicyRevision > gen.policyRevision {
			gen.policyRevision = item.PolicyRevision
		}
		ordinal := item.Ordinal
		if ordinal < 0 {
			ordinal = 0
		}
		gen.ports[ordinal] = item
	}
	return ledger
}

func normalizeAllocationState(state string) string {
	switch state {
	case model.PortAllocationStatePreparing, model.PortAllocationStateRetiring, model.PortAllocationStateActive:
		return state
	default:
		return model.PortAllocationStateActive
	}
}

func (l *ProxyPathPortLedger) ownerFor(kind, scopeKey string, serverID int64) *proxyPathPortOwner {
	key := proxyPathPortKey{Kind: kind, ScopeKey: scopeKey, ServerID: serverID}
	owner, ok := l.owners[key]
	if !ok {
		owner = &proxyPathPortOwner{key: key}
		l.owners[key] = owner
	}
	return owner
}

func (o *proxyPathPortOwner) generationFor(generation int) *proxyPathPortGeneration {
	for _, gen := range o.generations {
		if gen.generation == generation {
			return gen
		}
	}
	gen := &proxyPathPortGeneration{generation: generation, state: model.PortAllocationStateActive, ports: map[int]model.ProxyPathPortAllocation{}}
	o.generations = append(o.generations, gen)
	sort.Slice(o.generations, func(i, j int) bool { return o.generations[i].generation < o.generations[j].generation })
	return gen
}

func (o *proxyPathPortOwner) nextGeneration() int {
	next := 1
	for _, gen := range o.generations {
		if gen.generation >= next {
			next = gen.generation + 1
		}
	}
	return next
}

func (o *proxyPathPortOwner) activeGeneration() *proxyPathPortGeneration {
	var active *proxyPathPortGeneration
	for _, gen := range o.generations {
		if gen.state != model.PortAllocationStateActive {
			continue
		}
		if active == nil || gen.generation > active.generation {
			active = gen
		}
	}
	return active
}

func (o *proxyPathPortOwner) generationByState(state string) *proxyPathPortGeneration {
	for _, gen := range o.generations {
		if gen.state == state {
			return gen
		}
	}
	return nil
}

func (g *proxyPathPortGeneration) primaryRow() (model.ProxyPathPortAllocation, bool) {
	var best model.ProxyPathPortAllocation
	found := false
	ordinals := make([]int, 0, len(g.ports))
	for ordinal := range g.ports {
		ordinals = append(ordinals, ordinal)
	}
	sort.Ints(ordinals)
	for _, ordinal := range ordinals {
		if !found || ordinal < best.Ordinal {
			best = g.ports[ordinal]
			found = true
		}
	}
	return best, found
}

func (l *ProxyPathPortLedger) recordPending(row model.ProxyPathPortAllocation, key proxyPathPortKey) {
	l.pending = append(l.pending, row)
	l.pendingByKey[key] = true
}

// resolve returns the port of the active generation for one listener,
// allocating through Allocate only when the owner has no active generation yet.
// A stored port is returned unchanged even when it no longer matches the
// current policy; only Controller's migration flow may move it. Metadata (pool,
// listen address, network) follows the requirement so Pending() can converge
// legacy rows.
func (l *ProxyPathPortLedger) resolve(requirement PortRequirement) int {
	if l == nil {
		if requirement.Allocate != nil {
			return requirement.Allocate()
		}
		return 0
	}
	key := proxyPathPortKey{Kind: requirement.Kind, ScopeKey: requirement.ScopeKey, ServerID: requirement.ServerID}
	owner := l.ownerFor(requirement.Kind, requirement.ScopeKey, requirement.ServerID)
	l.used[key] = true
	if gen := owner.activeGeneration(); gen != nil {
		item, ok := gen.primaryRow()
		if !ok {
			return 0
		}
		if metadataDiverges(item, requirement) {
			corrected := item
			corrected.Pool = requirement.Pool
			corrected.ListenIP = requirement.ListenIP
			corrected.Network = forwardNetworkName(requirement.Network)
			gen.ports[item.Ordinal] = corrected
			l.recordPending(corrected, key)
		}
		return item.Port
	}
	port := 0
	if requirement.AllocateOrdinal != nil {
		port = requirement.AllocateOrdinal(0)
	}
	if port <= 0 && requirement.Allocate != nil {
		port = requirement.Allocate()
	}
	if port <= 0 {
		return 0
	}
	generation := owner.nextGeneration()
	gen := &proxyPathPortGeneration{generation: generation, state: model.PortAllocationStateActive, policyRevision: requirement.PolicyRevision, ports: map[int]model.ProxyPathPortAllocation{}}
	row := model.ProxyPathPortAllocation{
		Kind:           requirement.Kind,
		ScopeKey:       requirement.ScopeKey,
		ServerID:       requirement.ServerID,
		Pool:           requirement.Pool,
		ListenIP:       requirement.ListenIP,
		Network:        forwardNetworkName(requirement.Network),
		Generation:     generation,
		Ordinal:        0,
		Port:           port,
		State:          model.PortAllocationStateActive,
		PolicyRevision: requirement.PolicyRevision,
	}
	gen.ports[0] = row
	owner.generations = append(owner.generations, gen)
	sort.Slice(owner.generations, func(i, j int) bool { return owner.generations[i].generation < owner.generations[j].generation })
	l.recordPending(row, key)
	return port
}

// AllocatePreparing reserves a fresh preparing generation for one owner while
// the active generation keeps running. count is the number of ordinals (1 for
// single-port listeners, more for atomic multiport owners such as Mieru). Every
// candidate port is probed before any row is recorded; a single failure leaves
// the ledger unchanged so the caller can abort the reservation without side
// effects. The new rows carry the requirement's policy revision so a later
// migration can prove which policy revision the generation was built for.
func (l *ProxyPathPortLedger) AllocatePreparing(requirement PortRequirement, count int) ([]model.ProxyPathPortAllocation, error) {
	if l == nil {
		return nil, errors.New("port ledger is nil")
	}
	if count <= 0 {
		count = 1
	}
	key := proxyPathPortKey{Kind: requirement.Kind, ScopeKey: requirement.ScopeKey, ServerID: requirement.ServerID}
	owner := l.ownerFor(requirement.Kind, requirement.ScopeKey, requirement.ServerID)
	if owner.activeGeneration() == nil {
		return nil, fmt.Errorf("owner %s/%s on server %d has no active generation to migrate", requirement.Kind, requirement.ScopeKey, requirement.ServerID)
	}
	candidates := make([]int, count)
	for ordinal := 0; ordinal < count; ordinal++ {
		port := 0
		if requirement.AllocateOrdinal != nil {
			port = requirement.AllocateOrdinal(ordinal)
		} else if requirement.Allocate != nil {
			port = requirement.Allocate()
		}
		if port <= 0 {
			return nil, fmt.Errorf("no port available in pool %s for %s/%s generation %d ordinal %d", requirement.Pool, requirement.Kind, requirement.ScopeKey, owner.nextGeneration(), ordinal)
		}
		candidates[ordinal] = port
	}
	generation := owner.nextGeneration()
	gen := &proxyPathPortGeneration{generation: generation, state: model.PortAllocationStatePreparing, policyRevision: requirement.PolicyRevision, ports: map[int]model.ProxyPathPortAllocation{}}
	rows := make([]model.ProxyPathPortAllocation, 0, count)
	for ordinal, port := range candidates {
		row := model.ProxyPathPortAllocation{
			Kind:           requirement.Kind,
			ScopeKey:       requirement.ScopeKey,
			ServerID:       requirement.ServerID,
			Pool:           requirement.Pool,
			ListenIP:       requirement.ListenIP,
			Network:        forwardNetworkName(requirement.Network),
			Generation:     generation,
			Ordinal:        ordinal,
			Port:           port,
			State:          model.PortAllocationStatePreparing,
			PolicyRevision: requirement.PolicyRevision,
		}
		gen.ports[ordinal] = row
		rows = append(rows, row)
	}
	owner.generations = append(owner.generations, gen)
	sort.Slice(owner.generations, func(i, j int) bool { return owner.generations[i].generation < owner.generations[j].generation })
	for _, row := range rows {
		l.recordPending(row, key)
	}
	return rows, nil
}

// RowsForPhase returns the rows one migration phase must render for an owner:
//   - prepare keeps the active generation and adds the preparing listeners;
//   - switch uses the preparing generation as the new target while the active
//     generation stays as a compatibility listener;
//   - retire renders the new active generation plus the old retiring one.
//
// Ordinary deployments never call this; resolve() reads the active generation
// only.
func (l *ProxyPathPortLedger) RowsForPhase(requirement PortRequirement, phase string) []model.ProxyPathPortAllocation {
	if l == nil {
		return nil
	}
	owner, ok := l.owners[proxyPathPortKey{Kind: requirement.Kind, ScopeKey: requirement.ScopeKey, ServerID: requirement.ServerID}]
	if !ok {
		return nil
	}
	var out []model.ProxyPathPortAllocation
	for _, gen := range owner.generations {
		if !generationInPhase(gen.state, phase) {
			continue
		}
		ordinals := make([]int, 0, len(gen.ports))
		for ordinal := range gen.ports {
			ordinals = append(ordinals, ordinal)
		}
		sort.Ints(ordinals)
		for _, ordinal := range ordinals {
			out = append(out, gen.ports[ordinal])
		}
	}
	return out
}

func generationInPhase(state, phase string) bool {
	switch phase {
	case PortMigrationPhasePrepare, PortMigrationPhaseSwitch:
		return state == model.PortAllocationStateActive || state == model.PortAllocationStatePreparing
	case PortMigrationPhaseRetire:
		return state == model.PortAllocationStateActive || state == model.PortAllocationStateRetiring
	default:
		return state == model.PortAllocationStateActive
	}
}

// ResolveForPhase returns the port a consumer should dial during one migration
// phase. Prepare leaves consumers on the active generation, switch moves them
// to the preparing generation, and retire keeps them on the (new) active
// generation.
func (l *ProxyPathPortLedger) ResolveForPhase(requirement PortRequirement, phase string) int {
	if l == nil {
		return 0
	}
	if phase != PortMigrationPhaseSwitch {
		return l.resolve(requirement)
	}
	owner, ok := l.owners[proxyPathPortKey{Kind: requirement.Kind, ScopeKey: requirement.ScopeKey, ServerID: requirement.ServerID}]
	if !ok {
		return 0
	}
	gen := owner.generationByState(model.PortAllocationStatePreparing)
	if gen == nil {
		return 0
	}
	item, ok := gen.primaryRow()
	if !ok {
		return 0
	}
	return item.Port
}

// PromotePreparing completes a migration cutover: the preparing generation
// becomes active and the previous active generation becomes retiring. Retiring
// rows stay in the ledger and the database until DeleteRetired removes them, so
// the old listeners keep serving during the grace period.
func (l *ProxyPathPortLedger) PromotePreparing(kind, scopeKey string, serverID int64) ([]model.ProxyPathPortAllocation, error) {
	if l == nil {
		return nil, errors.New("port ledger is nil")
	}
	owner, ok := l.owners[proxyPathPortKey{Kind: kind, ScopeKey: scopeKey, ServerID: serverID}]
	if !ok {
		return nil, fmt.Errorf("owner %s/%s on server %d has no generations", kind, scopeKey, serverID)
	}
	preparing := owner.generationByState(model.PortAllocationStatePreparing)
	if preparing == nil {
		return nil, fmt.Errorf("owner %s/%s on server %d has no preparing generation", kind, scopeKey, serverID)
	}
	active := owner.activeGeneration()
	preparing.state = model.PortAllocationStateActive
	var out []model.ProxyPathPortAllocation
	for ordinal, item := range preparing.ports {
		item.State = model.PortAllocationStateActive
		preparing.ports[ordinal] = item
		out = append(out, item)
		l.recordPending(item, owner.key)
	}
	if active != nil {
		active.state = model.PortAllocationStateRetiring
		for ordinal, item := range active.ports {
			item.State = model.PortAllocationStateRetiring
			active.ports[ordinal] = item
			out = append(out, item)
			l.recordPending(item, owner.key)
		}
	}
	return out, nil
}

// MarkActiveRetiring flags the current active generation as retiring without
// promoting anything; used when a cutover must first move consumers off the old
// listeners and the new generation is not ready to promote yet.
func (l *ProxyPathPortLedger) MarkActiveRetiring(kind, scopeKey string, serverID int64) ([]model.ProxyPathPortAllocation, error) {
	if l == nil {
		return nil, errors.New("port ledger is nil")
	}
	owner, ok := l.owners[proxyPathPortKey{Kind: kind, ScopeKey: scopeKey, ServerID: serverID}]
	if !ok {
		return nil, fmt.Errorf("owner %s/%s on server %d has no generations", kind, scopeKey, serverID)
	}
	active := owner.activeGeneration()
	if active == nil {
		return nil, fmt.Errorf("owner %s/%s on server %d has no active generation to retire", kind, scopeKey, serverID)
	}
	active.state = model.PortAllocationStateRetiring
	var out []model.ProxyPathPortAllocation
	for ordinal, item := range active.ports {
		item.State = model.PortAllocationStateRetiring
		active.ports[ordinal] = item
		out = append(out, item)
		l.recordPending(item, owner.key)
	}
	return out, nil
}

// DeleteRetired removes one retiring generation from the ledger and marks its
// persisted row IDs for deletion. It is the only operation that may drop a
// generation, and only after the new active generation is verified; stale
// cleanup never deletes a retiring generation on its own.
func (l *ProxyPathPortLedger) DeleteRetired(kind, scopeKey string, serverID int64, generation int) ([]int64, error) {
	if l == nil {
		return nil, errors.New("port ledger is nil")
	}
	owner, ok := l.owners[proxyPathPortKey{Kind: kind, ScopeKey: scopeKey, ServerID: serverID}]
	if !ok {
		return nil, fmt.Errorf("owner %s/%s on server %d has no generations", kind, scopeKey, serverID)
	}
	var target *proxyPathPortGeneration
	for _, gen := range owner.generations {
		if gen.generation == generation {
			target = gen
		}
	}
	if target == nil {
		return nil, fmt.Errorf("owner %s/%s on server %d has no generation %d", kind, scopeKey, serverID, generation)
	}
	if target.state != model.PortAllocationStateRetiring {
		return nil, fmt.Errorf("owner %s/%s on server %d generation %d is %s, not retiring", kind, scopeKey, serverID, generation, target.state)
	}
	var ids []int64
	for _, item := range target.ports {
		if item.ID > 0 && !l.removed[item.ID] {
			l.removed[item.ID] = true
			ids = append(ids, item.ID)
		}
	}
	remaining := owner.generations[:0]
	for _, gen := range owner.generations {
		if gen != target {
			remaining = append(remaining, gen)
		}
	}
	owner.generations = remaining
	return ids, nil
}

func metadataDiverges(stored model.ProxyPathPortAllocation, requirement PortRequirement) bool {
	return stored.Pool != requirement.Pool ||
		stored.ListenIP != requirement.ListenIP ||
		stored.Network != forwardNetworkName(requirement.Network)
}

func forwardNetworkName(protocol model.ForwardProtocol) string {
	switch protocol {
	case model.ForwardProtocolTCP:
		return "tcp"
	case model.ForwardProtocolUDP:
		return "udp"
	default:
		return "tcp_udp"
	}
}

// markProjectionComplete records that a projection enumerated every enabled path
// without aborting, so the set of resolved listeners is authoritative.
func (l *ProxyPathPortLedger) markProjectionComplete() {
	if l != nil {
		l.complete = true
	}
}

// StaleProxyPathPortAllocationIDs reports the persisted records no listener
// claims anymore, so Controller can release those ports for future allocation.
//
// An owner is claimed when any of its generations resolved in the current
// projection; every generation of a claimed owner survives, because preparing
// and retiring rows are owned by the migration flow, not by stale cleanup. An
// unclaimed owner releases every generation. Rows explicitly deleted by
// DeleteRetired are always reported.
//
// Releasing requires a complete projection. A run that aborted partway resolved
// only some listeners, and treating that partial view as authoritative would drop
// allocations that are still deployed.
func StaleProxyPathPortAllocationIDs(stored []model.ProxyPathPortAllocation, ledger *ProxyPathPortLedger) []int64 {
	if ledger == nil || !ledger.complete {
		return nil
	}
	out := []int64{}
	for _, item := range stored {
		if ledger.removed[item.ID] {
			out = append(out, item.ID)
			continue
		}
		key := proxyPathPortKey{Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID}
		if ledger.used[key] {
			continue
		}
		if ledger.pendingByKey[key] {
			continue
		}
		out = append(out, item.ID)
	}
	return out
}

// Pending returns the allocations this projection created or corrected, in
// allocation order. Corrections carry the same port as the stored row and only
// update metadata; the store upsert is idempotent for both.
func (l *ProxyPathPortLedger) Pending() []model.ProxyPathPortAllocation {
	if l == nil {
		return nil
	}
	out := make([]model.ProxyPathPortAllocation, 0, len(l.pending))
	out = append(out, l.pending...)
	return out
}

type proxyPathChainServiceKey struct {
	ServerID int64
	Protocol model.Protocol
	Profile  string
}

type proxyPathChainService struct {
	Key         proxyPathChainServiceKey
	ChainConfig ProxyPathChainConfig
	Inbound     model.Inbound
	Tag         string
	Users       []model.User
}

type ProxyPathChainConfig struct {
	Protocol               model.Protocol
	Method                 string
	RealityHandshakeServer string
	RealityHandshakePort   int
}

func ValidateProxyPathChainMethod(method string) error {
	method = normalizeProxyPathChainMethod(method)
	if _, ok := proxyPathChainMethods[method]; !ok {
		return fmt.Errorf("unsupported proxy path Shadowsocks method %q", method)
	}
	return nil
}

func normalizeProxyPathChainMethod(method string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		return DefaultProxyPathChainMethod
	}
	return method
}

func proxyPathStepChainMethod(step model.ProxyPathStep) string {
	return normalizeProxyPathChainMethod(stringValue(parseStepConfig(step.ConfigJSON), "chain_method", ""))
}

func ParseProxyPathChainConfig(raw string) (ProxyPathChainConfig, error) {
	cfg := parseStepConfig(raw)
	protocol := model.Protocol(strings.ToLower(strings.TrimSpace(stringValue(cfg, "chain_protocol", string(model.ProtocolSS)))))
	if protocol == "" {
		protocol = model.ProtocolSS
	}
	out := ProxyPathChainConfig{Protocol: protocol}
	switch protocol {
	case model.ProtocolSS:
		out.Method = normalizeProxyPathChainMethod(stringValue(cfg, "chain_method", ""))
		if err := ValidateProxyPathChainMethod(out.Method); err != nil {
			return ProxyPathChainConfig{}, err
		}
	case model.ProtocolVLESS:
		out.RealityHandshakeServer = strings.ToLower(strings.TrimSpace(stringValue(cfg, "reality_handshake_server", DefaultProxyPathRealityHandshakeServer)))
		out.RealityHandshakePort = intValueFromMap(cfg, "reality_handshake_port", DefaultProxyPathRealityHandshakePort)
		if err := ValidateSafeHost(out.RealityHandshakeServer); err != nil {
			return ProxyPathChainConfig{}, fmt.Errorf("Reality handshake server: %w", err)
		}
		if err := ValidatePort(out.RealityHandshakePort); err != nil {
			return ProxyPathChainConfig{}, fmt.Errorf("Reality handshake port: %w", err)
		}
	case model.ProtocolMieru, model.ProtocolSocks:
	default:
		return ProxyPathChainConfig{}, fmt.Errorf("unsupported generated proxy path protocol %q", protocol)
	}
	return out, nil
}

func proxyPathStepChainConfig(step model.ProxyPathStep) (ProxyPathChainConfig, error) {
	return ParseProxyPathChainConfig(step.ConfigJSON)
}

func (c ProxyPathChainConfig) profile() string {
	switch c.Protocol {
	case model.ProtocolVLESS:
		return fmt.Sprintf("reality:%s:%d", c.RealityHandshakeServer, c.RealityHandshakePort)
	case model.ProtocolMieru:
		return "tcp"
	case model.ProtocolSocks:
		return "socks5"
	default:
		return c.Method
	}
}

func proxyPathChainServiceKeyForStep(step model.ProxyPathStep, serverID int64) (proxyPathChainServiceKey, error) {
	cfg, err := proxyPathStepChainConfig(step)
	if err != nil {
		return proxyPathChainServiceKey{}, err
	}
	return proxyPathChainServiceKey{ServerID: serverID, Protocol: cfg.Protocol, Profile: cfg.profile()}, nil
}

func buildProxyPathChainServices(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) (map[proxyPathChainServiceKey]*proxyPathChainService, error) {
	serverByID := make(map[int64]model.Server, len(servers))
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	pathByID := make(map[int64]model.ProxyPath, len(paths))
	for _, path := range paths {
		pathByID[path.ID] = path
	}
	orderedSteps := append([]model.ProxyPathStep(nil), steps...)
	sort.SliceStable(orderedSteps, func(i, j int) bool {
		if orderedSteps[i].PathID == orderedSteps[j].PathID {
			if orderedSteps[i].Position == orderedSteps[j].Position {
				return orderedSteps[i].ID < orderedSteps[j].ID
			}
			return orderedSteps[i].Position < orderedSteps[j].Position
		}
		return orderedSteps[i].PathID < orderedSteps[j].PathID
	})
	services := map[proxyPathChainServiceKey]*proxyPathChainService{}
	for _, step := range orderedSteps {
		path, ok := pathByID[step.PathID]
		if !ok || !path.Enabled || step.NodeType != model.ProxyPathStepServerInbound || (step.InboundID != nil && *step.InboundID != 0) {
			continue
		}
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode == model.ProxyPathTransportPortForward || step.ServerID == nil || *step.ServerID == 0 {
			continue
		}
		chainConfig, err := proxyPathStepChainConfig(step)
		if err != nil {
			return nil, fmt.Errorf("proxy path %s step %d: %w", path.Name, step.Position, err)
		}
		server, ok := serverByID[*step.ServerID]
		if !ok {
			return nil, fmt.Errorf("proxy path %s step %d: target server not found", path.Name, step.Position)
		}
		key := proxyPathChainServiceKey{ServerID: server.ID, Protocol: chainConfig.Protocol, Profile: chainConfig.profile()}
		service := services[key]
		if service == nil {
			service = &proxyPathChainService{Key: key, ChainConfig: chainConfig}
			services[key] = service
		}
		service.Users = append(service.Users, proxyPathInternalUser(path, step))
	}

	occupied := make(map[int64]model.Inbound, len(inbounds)+len(services))
	for _, inbound := range inbounds {
		occupied[inbound.ID] = inbound
	}
	keys := make([]proxyPathChainServiceKey, 0, len(services))
	for key := range services {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].ServerID == keys[j].ServerID {
			if keys[i].Protocol == keys[j].Protocol {
				return keys[i].Profile < keys[j].Profile
			}
			return keys[i].Protocol < keys[j].Protocol
		}
		return keys[i].ServerID < keys[j].ServerID
	})
	for _, key := range keys {
		service := services[key]
		server := serverByID[key.ServerID]
		seed := int(stableProxyPathResourceID("proxy-path-chain-service", key.Protocol, key.Profile) % 1000000)
		start, end := proxyPathServerPortRange(server)
		portProtocol := model.ForwardProtocolTCP
		if key.Protocol == model.ProtocolSS || key.Protocol == model.ProtocolSocks {
			portProtocol = model.ForwardProtocolTCPUDP
		}
		port := ledger.resolve(PortRequirement{
			Kind:           model.ProxyPathPortKindChainService,
			ScopeKey:       proxyPathChainServiceScopeKey(key),
			ServerID:       server.ID,
			Pool:           model.PortPoolPublic,
			ListenIP:       firstNonEmpty(server.ListenIP, "0.0.0.0"),
			Network:        portProtocol,
			PolicyRevision: serverPortPolicyRevision(server),
			Allocate: func() int {
				return proxyPathAvailablePortForProtocol(server, server.ID*977, seed, start, end, portProtocol, firstNonEmpty(server.ListenIP, "0.0.0.0"), occupied)
			},
		})
		if port == 0 {
			return nil, fmt.Errorf("server %s has no available port in the managed public range %d-%d for shared %s chain service", server.Name, start, end, key.Protocol)
		}
		service.Tag = proxyPathChainServiceTag(key)
		configJSON, err := proxyPathChainServiceConfigJSON(server, key, service.ChainConfig)
		if err != nil {
			return nil, err
		}
		service.Inbound = model.Inbound{
			ID:         proxyPathChainServiceID(key),
			ServerID:   key.ServerID,
			Name:       fmt.Sprintf("共享链路 / %s", proxyPathChainServiceLabel(key)),
			Protocol:   key.Protocol,
			ListenIP:   firstNonEmpty(server.ListenIP, "0.0.0.0"),
			Port:       port,
			ConfigJSON: string(configJSON),
			Enabled:    true,
		}
		occupied[service.Inbound.ID] = service.Inbound
	}
	return services, nil
}

func proxyPathChainServiceForStep(services map[proxyPathChainServiceKey]*proxyPathChainService, step model.ProxyPathStep, targetServerID int64) (*proxyPathChainService, bool) {
	if step.InboundID != nil && *step.InboundID != 0 {
		return nil, false
	}
	mode := step.TransportMode
	if mode == "" {
		mode = model.ProxyPathTransportSingBox
	}
	if mode == model.ProxyPathTransportPortForward {
		return nil, false
	}
	key, err := proxyPathChainServiceKeyForStep(step, targetServerID)
	if err != nil {
		return nil, false
	}
	service, ok := services[key]
	return service, ok
}

func proxyPathChainServiceScopeKey(key proxyPathChainServiceKey) string {
	if key.Protocol == model.ProtocolSS {
		return key.Profile
	}
	return string(key.Protocol) + ":" + key.Profile
}

func proxyPathChainServiceTag(key proxyPathChainServiceKey) string {
	if key.Protocol == model.ProtocolSS {
		return "oboard-chain-ss-" + strings.NewReplacer("2022-blake3-", "", "-gcm", "", "-poly1305", "").Replace(key.Profile) + "-in"
	}
	sum := sha256.Sum256([]byte(key.Profile))
	return fmt.Sprintf("oboard-chain-%s-%s-in", key.Protocol, hex.EncodeToString(sum[:4]))
}

func proxyPathChainServiceID(key proxyPathChainServiceKey) int64 {
	if key.Protocol == model.ProtocolSS {
		return -(int64(1)<<41 + (key.ServerID&0xffffff)<<4 + int64(proxyPathChainMethods[normalizeProxyPathChainMethod(key.Profile)])&0xf)
	}
	return -stableProxyPathResourceID("proxy-path-chain-inbound", key.ServerID, key.Protocol, key.Profile)
}

func proxyPathChainServiceLabel(key proxyPathChainServiceKey) string {
	switch key.Protocol {
	case model.ProtocolVLESS:
		return "VLESS Reality"
	case model.ProtocolMieru:
		return "Mieru TCP"
	case model.ProtocolSocks:
		return "SOCKS5"
	default:
		return key.Profile
	}
}

func proxyPathChainServicePassword(server model.Server, method string) string {
	seed := proxyPathServerChainSeed(server)
	sum := sha256.Sum256([]byte("oboard-chain-ss:" + seed + ":" + normalizeProxyPathChainMethod(method)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func proxyPathChainServiceConfigJSON(server model.Server, key proxyPathChainServiceKey, chainConfig ProxyPathChainConfig) (string, error) {
	var cfg map[string]any
	switch key.Protocol {
	case model.ProtocolSS:
		cfg = map[string]any{"method": key.Profile, "password": proxyPathChainServicePassword(server, key.Profile)}
	case model.ProtocolMieru:
		cfg = map[string]any{"transport": "TCP", "multiplexing": "MULTIPLEXING_DEFAULT", "user_hint_is_mandatory": true}
	case model.ProtocolSocks:
		cfg = map[string]any{}
	case model.ProtocolVLESS:
		privateSeed := sha256.Sum256([]byte("oboard-chain-vless-private:" + proxyPathServerChainSeed(server) + ":" + key.Profile))
		privateKey, err := ecdh.X25519().NewPrivateKey(privateSeed[:])
		if err != nil {
			return "", err
		}
		shortSeed := sha256.Sum256([]byte("oboard-chain-vless-short-id:" + proxyPathServerChainSeed(server) + ":" + key.Profile))
		cfg = map[string]any{
			"flow": "xtls-rprx-vision",
			"tls": map[string]any{
				"enabled":     true,
				"server_name": chainConfig.RealityHandshakeServer,
				"reality": map[string]any{
					"enabled":     true,
					"private_key": base64.RawURLEncoding.EncodeToString(privateKey.Bytes()),
					"public_key":  base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
					"short_id":    hex.EncodeToString(shortSeed[:4]),
					"handshake": map[string]any{
						"server":      chainConfig.RealityHandshakeServer,
						"server_port": chainConfig.RealityHandshakePort,
					},
				},
			},
		}
	default:
		return "", fmt.Errorf("unsupported generated proxy path protocol %q", key.Protocol)
	}
	b, err := json.Marshal(cfg)
	return string(b), err
}

func stableProxyPathResourceID(kind string, values ...any) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(kind))
	for _, value := range values {
		_, _ = h.Write([]byte(fmt.Sprintf("|%v", value)))
	}
	return 850000000000 + int64(h.Sum64()%40000000000)
}

func BuildProxyPathPlans(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound) ([]model.ProxyPathPlan, error) {
	plans, _, err := buildProxyPathPlansWithInbounds(paths, steps, servers, inbounds, nil)
	return plans, err
}

// BuildProxyPathPlansWithLedger projects the paths while preferring persisted
// generated-listener ports. Newly allocated ports are recorded on the ledger for
// the caller to persist once the deployment is accepted.
func BuildProxyPathPlansWithLedger(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.ProxyPathPlan, error) {
	plans, _, err := buildProxyPathPlansWithInbounds(paths, steps, servers, inbounds, ledger)
	return plans, err
}

// buildProxyPathPlansWithInbounds also returns the synthetic inbound table the
// projection allocated, keyed by inbound ID. Config generation must reuse this
// table instead of recomputing ports: the allocator picks the first free port
// from a seed, so a different occupancy set would silently yield a different
// port and the derived forward would target a listener nobody owns.
func buildProxyPathPlansWithInbounds(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.ProxyPathPlan, map[int64]model.Inbound, error) {
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	serverByID := map[int64]model.Server{}
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	if err := validateProxyPathTransportSet(paths, stepsByPath, inboundByID); err != nil {
		return nil, nil, err
	}
	transparentGroups := buildTransparentProxyPathGroups(paths, stepsByPath)
	chainServices, err := buildProxyPathChainServices(paths, steps, servers, inbounds, ledger)
	if err != nil {
		return nil, nil, err
	}
	for _, service := range chainServices {
		inboundByID[service.Inbound.ID] = service.Inbound
	}
	sharedTunnels := map[string]model.Tunnel{}
	orderedPaths := append([]model.ProxyPath(nil), paths...)
	sort.SliceStable(orderedPaths, func(i, j int) bool { return orderedPaths[i].ID < orderedPaths[j].ID })
	out := []model.ProxyPathPlan{}
	for _, path := range orderedPaths {
		pathSteps := append([]model.ProxyPathStep(nil), stepsByPath[path.ID]...)
		sort.SliceStable(pathSteps, func(i, j int) bool {
			if pathSteps[i].Position == pathSteps[j].Position {
				return pathSteps[i].ID < pathSteps[j].ID
			}
			return pathSteps[i].Position < pathSteps[j].Position
		})
		plan := model.ProxyPathPlan{PathID: path.ID, Name: path.Name, InboundID: path.InboundID, Enabled: path.Enabled}
		root, ok := inboundByID[path.InboundID]
		if IsFamilyBranch(path) {
			if err := ValidateFamilyBranchTransport(pathSteps); err != nil {
				if path.Enabled {
					return nil, nil, fmt.Errorf("代理路径 %s: %w", path.Name, err)
				}
				plan.Warnings = append(plan.Warnings, err.Error())
			}
			if !ok {
				root = model.Inbound{Name: path.Name, Enabled: true}
				ok = true
			}
		}
		if !ok {
			if path.Enabled {
				return nil, nil, fmt.Errorf("代理路径 %s 的入口不存在", path.Name)
			}
			plan.Warnings = append(plan.Warnings, "入口不存在")
			out = append(out, plan)
			continue
		}
		previousServerID := root.ServerID
		sourceListenPort := root.Port
		processingCount := 0
		for _, step := range pathSteps {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			plan.Steps = append(plan.Steps, model.ProxyPathPlanStep{ID: step.ID, Position: step.Position, NodeType: step.NodeType, TransportMode: mode, ProcessingRole: step.ProcessingRole, ServerID: step.ServerID, InboundID: step.InboundID, ExternalOutboundID: step.ExternalOutboundID})
			planStepIndex := len(plan.Steps) - 1
			if step.ProcessingRole {
				processingCount++
			}
			targetServerID, targetInbound, targetOK := proxyPathStepTargetServer(step, inboundByID)
			var plannedInbound model.Inbound
			if targetOK {
				plannedInbound = proxyPathPlanTargetInbound(path, step, targetServerID, targetInbound, serverByID, inboundByID, chainServices, transparentGroups[path.ID], ledger)
				if step.TransportMode == model.ProxyPathTransportPortForward && step.ProcessingRole {
					plannedInbound.Protocol = root.Protocol
					plannedInbound.ConfigJSON = root.ConfigJSON
				}
				// Only an enabled path reserves runtime ports. A disabled branch
				// deploys nothing, so letting it occupy a port would make enabling or
				// disabling it silently change the allocation of unrelated paths.
				if path.Enabled {
					inboundByID[plannedInbound.ID] = plannedInbound
					if targetInbound == nil {
						plan.RuntimeNodes = append(plan.RuntimeNodes, proxyPathRuntimeTargetNode(path, step, targetServerID, plannedInbound, chainServices, transparentGroups[path.ID]))
					}
				}
				if path.Enabled && mode == model.ProxyPathTransportSingBox && previousServerID != 0 {
					sourceServer, sourceOK := serverByID[previousServerID]
					targetServer, targetServerOK := serverByID[targetServerID]
					if !sourceOK || !targetServerOK {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳无法确定源/目标服务器", path.Name, step.Position)
					}
					if _, err := ResolveReachableEntryAddress(sourceServer, plannedInbound, targetServer); err != nil {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳: %w", path.Name, step.Position, err)
					}
				}
			}
			switch mode {
			case model.ProxyPathTransportPortForward:
				if !targetOK {
					if path.Enabled {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳端口转发需要目标服务器", path.Name, step.Position)
					}
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("第 %d 跳端口转发需要目标服务器", step.Position))
					continue
				}
				f, err := proxyPathManagedPortForward(path, step, root, previousServerID, targetServerID, sourceListenPort, plannedInbound, serverByID, transparentGroups[path.ID])
				if err != nil {
					if path.Enabled {
						return nil, nil, err
					}
					plan.Warnings = append(plan.Warnings, err.Error())
					continue
				}
				plan.PortForwards = append(plan.PortForwards, f)
				if path.Enabled && step.ProcessingRole {
					processingInbound := root
					processingInbound.ID = plannedInbound.ID
					processingInbound.ServerID = targetServerID
					processingInbound.Name = fmt.Sprintf("%s / 处理加解密", firstNonEmpty(path.Name, root.Name))
					group := transparentGroups[path.ID]
					resourceKey := fmt.Sprintf("trusted-inner:path:%d:step:%d", path.ID, step.Position)
					shared := false
					if group != nil {
						processingInbound.Name = fmt.Sprintf("%s / 共享处理加解密", firstNonEmpty(root.Name, fmt.Sprintf("入口 %d", root.ID)))
						processingInbound = proxyPathSharedTrustedInnerInbound(group.InboundID, group.PrefixLength, serverByID[targetServerID], processingInbound, inboundByID, ledger)
						resourceKey = fmt.Sprintf("trusted-inner:inbound:%d:step:%d", group.InboundID, group.PrefixLength)
						shared = true
					} else {
						processingInbound = proxyPathTrustedInnerInbound(path, step, serverByID[targetServerID], processingInbound, inboundByID, ledger)
					}
					// Keep the loopback listener in the projection's occupancy set. The
					// outer transparent listener keeps its generated ID, so reserve a
					// disjoint internal ID for collision checks performed by later paths.
					processingReservation := processingInbound
					processingReservation.ID = processingInbound.ID - (int64(1) << 44)
					inboundByID[processingReservation.ID] = processingReservation
					plan.RuntimeNodes = append(plan.RuntimeNodes, proxyPathRuntimeNode(resourceKey, step.ID, "trusted_processing_inbound", processingInbound, "", shared))
				}
				sourceListenPort = plannedInbound.Port
			case model.ProxyPathTransportTunnel:
				if !targetOK {
					if path.Enabled {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳隧道需要目标服务器", path.Name, step.Position)
					}
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("第 %d 跳隧道需要目标服务器", step.Position))
					continue
				}
				tunnelKey := proxyPathTunnelReuseKey(step, previousServerID, targetServerID, plannedInbound)
				t, exists := sharedTunnels[tunnelKey]
				if !exists {
					var err error
					t, err = proxyPathManagedTunnel(path, step, previousServerID, targetServerID, plannedInbound, serverByID, inboundByID, ledger)
					if err != nil {
						if path.Enabled {
							return nil, nil, err
						}
						plan.Warnings = append(plan.Warnings, err.Error())
						continue
					}
					sharedTunnels[tunnelKey] = t
					reserveProxyPathTunnelPorts(t, inboundByID)
				}
				plan.Tunnels = append(plan.Tunnels, t)
				plan.Steps[planStepIndex].TunnelID = t.ID
			}
			if targetOK {
				previousServerID = targetServerID
			}
		}
		_ = processingCount // processing_role is an internal marker for a transparent prefix.
		if path.Enabled && processingCount == 1 && len(plan.PortForwards) > 0 {
			for _, step := range pathSteps {
				if !step.ProcessingRole {
					continue
				}
				processingServerID, _, ok := proxyPathStepTargetServer(step, inboundByID)
				entryServer, entryOK := serverByID[root.ServerID]
				processingServer, processingOK := serverByID[processingServerID]
				if !ok || !entryOK || !processingOK {
					return nil, nil, fmt.Errorf("代理路径 %s 无法生成可信转发凭据", path.Name)
				}
				group := transparentGroups[path.ID]
				receiverID := proxyPathTrustedForwardReceiverID(path.ID, step.ID)
				key := proxyPathTrustedForwardKey(entryServer, processingServer, path.ID, step.ID)
				if group != nil {
					receiverID = proxyPathSharedTrustedForwardReceiverID(group.InboundID, group.PrefixLength)
					key = proxyPathSharedTrustedForwardKey(entryServer, processingServer, group.InboundID, group.PrefixLength)
				}
				plan.PortForwards[0].TrustedForward = &model.TrustedForwardSender{
					Version:             1,
					ReceiverID:          receiverID,
					Key:                 key,
					MaxClockSkewSeconds: 120,
				}
				break
			}
		}
		out = append(out, plan)
	}
	finalizeProxyPathRuntimeNodeReferences(out)
	ledger.markProjectionComplete()
	return out, inboundByID, nil
}

type transparentProxyPathGroup struct {
	InboundID    int64
	PrefixLength int
	OwnerPathID  int64
	Paths        []model.ProxyPath
}

func buildTransparentProxyPathGroups(paths []model.ProxyPath, stepsByPath map[int64][]model.ProxyPathStep) map[int64]*transparentProxyPathGroup {
	byInbound := map[int64]*transparentProxyPathGroup{}
	byPath := map[int64]*transparentProxyPathGroup{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		prefixLength := transparentProxyPathPrefixLength(orderedProxyPathSteps(stepsByPath[path.ID]))
		if prefixLength == 0 {
			continue
		}
		group := byInbound[path.InboundID]
		if group == nil {
			group = &transparentProxyPathGroup{InboundID: path.InboundID, PrefixLength: prefixLength, OwnerPathID: path.ID}
			byInbound[path.InboundID] = group
		}
		if path.ID < group.OwnerPathID {
			group.OwnerPathID = path.ID
		}
		group.Paths = append(group.Paths, path)
		byPath[path.ID] = group
	}
	for _, group := range byInbound {
		sort.SliceStable(group.Paths, func(i, j int) bool { return group.Paths[i].ID < group.Paths[j].ID })
	}
	return byPath
}

func transparentProxyPathPrefixLength(steps []model.ProxyPathStep) int {
	length := 0
	for _, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode != model.ProxyPathTransportPortForward {
			break
		}
		length++
	}
	return length
}

func transparentProxyPathPrefixSignature(steps []model.ProxyPathStep) string {
	type transparentStep struct {
		ServerID   *int64 `json:"server_id,omitempty"`
		InboundID  *int64 `json:"inbound_id,omitempty"`
		ConfigJSON string `json:"config_json"`
	}
	prefix := make([]transparentStep, 0)
	for _, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode != model.ProxyPathTransportPortForward {
			break
		}
		prefix = append(prefix, transparentStep{ServerID: step.ServerID, InboundID: step.InboundID, ConfigJSON: canonicalJSONObject(step.ConfigJSON)})
	}
	if len(prefix) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(prefix)
	return string(encoded)
}

func validateProxyPathTransportSet(paths []model.ProxyPath, stepsByPath map[int64][]model.ProxyPathStep, inboundByID map[int64]model.Inbound) error {
	enabledByInbound := map[int64][]model.ProxyPath{}
	transparentSignatureByInbound := map[int64]string{}
	transparentCountByInbound := map[int64]int{}
	directSignatures := map[string]model.ProxyPath{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		if IsFamilyBranch(path) {
			if err := ValidateFamilyBranchTransport(ordered); err != nil {
				return fmt.Errorf("代理路径 %s: %w", path.Name, err)
			}
			continue
		}
		root, ok := inboundByID[path.InboundID]
		if !ok {
			continue
		}
		enabledByInbound[path.InboundID] = append(enabledByInbound[path.InboundID], path)
		for _, step := range ordered {
			if step.InboundID == nil || *step.InboundID == 0 {
				continue
			}
			target, ok := inboundByID[*step.InboundID]
			if !ok || target.Protocol != model.ProtocolMieru {
				continue
			}
			ports, err := MieruInboundPorts(target)
			if err != nil {
				return fmt.Errorf("代理路径 %s 的 Mieru 节点端口无效：%w", path.Name, err)
			}
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if len(ports) > 1 && (mode == model.ProxyPathTransportPortForward || mode == model.ProxyPathTransportTunnel) {
				return fmt.Errorf("代理路径 %s 的多端口 Mieru 节点只能使用 sing-box 出站链", path.Name)
			}
		}
		for index, step := range ordered {
			if step.NodeType != model.ProxyPathStepWARP {
				continue
			}
			if path.Kind != "" && path.Kind != model.ProxyPathKindChain && path.Kind != model.ProxyPathKindFamilyBranch {
				return fmt.Errorf("WARP 只能作为普通代理拓扑或双栈模板的出口")
			}
			if index != len(ordered)-1 {
				return fmt.Errorf("代理路径 %s 的 WARP 必须是最后一个节点", path.Name)
			}
			if step.TransportMode != "" && step.TransportMode != model.ProxyPathTransportSingBox {
				return fmt.Errorf("代理路径 %s 的 WARP 只能使用 sing-box 出站", path.Name)
			}
			if index > 0 && ordered[index-1].NodeType != model.ProxyPathStepServerInbound {
				return fmt.Errorf("代理路径 %s 的 WARP 必须直接连接在可控服务器之后", path.Name)
			}
		}
		if path.Kind == model.ProxyPathKindDirect {
			if len(ordered) > 0 {
				last := ordered[len(ordered)-1]
				if last.NodeType != model.ProxyPathStepServerInbound {
					return fmt.Errorf("直接出口分支 %s 的最后一个节点必须是可控服务器", path.Name)
				}
				if _, _, ok := proxyPathStepTargetServer(last, inboundByID); !ok {
					return fmt.Errorf("直接出口分支 %s 的出口服务器不存在", path.Name)
				}
			}
			signature := directProxyPathSignature(path.InboundID, ordered)
			if previous, exists := directSignatures[signature]; exists {
				previousName := strings.TrimSpace(previous.Name)
				if previousName == "" {
					previousName = fmt.Sprintf("#%d", previous.ID)
				} else {
					previousName = fmt.Sprintf("%s (#%d)", previousName, previous.ID)
				}
				pathName := strings.TrimSpace(path.Name)
				if pathName == "" {
					pathName = fmt.Sprintf("#%d", path.ID)
				} else {
					pathName = fmt.Sprintf("%s (#%d)", pathName, path.ID)
				}
				return fmt.Errorf("入口 %d 的直接出口分支「%s」与「%s」位于同一位置；请删除或停用其中一条后再同步", path.InboundID, previousName, pathName)
			}
			directSignatures[signature] = path
		}
		transparent, err := validateProxyPathTransportSemantics(path, root, ordered)
		if err != nil {
			return err
		}
		if transparent {
			signature := transparentProxyPathPrefixSignature(ordered)
			if previous := transparentSignatureByInbound[path.InboundID]; previous != "" && previous != signature {
				return fmt.Errorf("入口 %d 的启用分支必须复用完全相同的透明转发前缀，并在处理加解密节点或其后分叉", path.InboundID)
			}
			transparentSignatureByInbound[path.InboundID] = signature
			transparentCountByInbound[path.InboundID]++
		}
	}
	for inboundID, signature := range transparentSignatureByInbound {
		pathsForInbound := enabledByInbound[inboundID]
		if signature == "" || transparentCountByInbound[inboundID] != len(pathsForInbound) {
			return fmt.Errorf("入口 %d 使用透明转发时，所有启用分支都必须复用相同前缀，不能在处理加解密节点之前分叉", inboundID)
		}
		if len(pathsForInbound) > 1 {
			root, ok := inboundByID[inboundID]
			if !ok || !InboundSupportsMultipleUsers(root) {
				return fmt.Errorf("入口 %d 的协议不支持通过多个用户名复用透明转发前缀", inboundID)
			}
		}
	}
	return nil
}

// ProxyPathWARPServerIDs resolves every enabled WARP terminal to the controlled
// server immediately before it. The same derivation drives config generation,
// deployment requests, and UI status so ownership cannot drift between layers.
func ProxyPathWARPServerIDs(paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) (map[int64]bool, error) {
	inboundByID := make(map[int64]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	out := map[int64]bool{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		currentServerID := int64(0)
		if IsFamilyBranch(path) {
			// Family-branch templates have no inbound; start from the first hop.
		} else {
			root, ok := inboundByID[path.InboundID]
			if !ok {
				return nil, fmt.Errorf("代理路径 %s 的入口不存在", path.Name)
			}
			currentServerID = root.ServerID
		}
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		for index, step := range ordered {
			switch step.NodeType {
			case model.ProxyPathStepServerInbound:
				serverID, _, ok := proxyPathStepTargetServer(step, inboundByID)
				if ok {
					currentServerID = serverID
				}
			case model.ProxyPathStepWARP:
				if index != len(ordered)-1 || (index > 0 && ordered[index-1].NodeType != model.ProxyPathStepServerInbound) {
					return nil, fmt.Errorf("代理路径 %s 的 WARP 必须直接连接在最后一台可控服务器之后", path.Name)
				}
				if currentServerID == 0 {
					return nil, fmt.Errorf("代理路径 %s 无法确定 WARP 出口服务器", path.Name)
				}
				out[currentServerID] = true
			}
		}
	}
	return out, nil
}

func directProxyPathSignature(inboundID int64, steps []model.ProxyPathStep) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d", inboundID)
	for _, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		fmt.Fprintf(&b, "|%s:%s:", step.NodeType, mode)
		if step.ServerID != nil {
			fmt.Fprintf(&b, "s%d", *step.ServerID)
		}
		if step.InboundID != nil {
			fmt.Fprintf(&b, "i%d", *step.InboundID)
		}
		if step.ExternalOutboundID != nil {
			fmt.Fprintf(&b, "e%d", *step.ExternalOutboundID)
		}
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(step.ConfigJSON))
	}
	return b.String()
}

type DuplicateDirectProxyPathConflict struct {
	InboundID int64
	PathIDs   []int64
}

// DuplicateDirectProxyPathConflicts reports enabled direct branches that occupy
// the same logical position. It intentionally requires no server projection so
// the automatic reconciler can isolate an invalid path set and continue
// preparing unrelated servers.
func DuplicateDirectProxyPathConflicts(paths []model.ProxyPath, steps []model.ProxyPathStep) []DuplicateDirectProxyPathConflict {
	stepsByPath := make(map[int64][]model.ProxyPathStep)
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	groups := make(map[string][]int64)
	inboundBySignature := make(map[string]int64)
	for _, path := range paths {
		if !path.Enabled || path.Kind != model.ProxyPathKindDirect {
			continue
		}
		signature := directProxyPathSignature(path.InboundID, orderedProxyPathSteps(stepsByPath[path.ID]))
		groups[signature] = append(groups[signature], path.ID)
		inboundBySignature[signature] = path.InboundID
	}
	out := make([]DuplicateDirectProxyPathConflict, 0)
	for signature, pathIDs := range groups {
		if len(pathIDs) < 2 {
			continue
		}
		sort.Slice(pathIDs, func(i, j int) bool { return pathIDs[i] < pathIDs[j] })
		out = append(out, DuplicateDirectProxyPathConflict{InboundID: inboundBySignature[signature], PathIDs: pathIDs})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InboundID == out[j].InboundID {
			return out[i].PathIDs[0] < out[j].PathIDs[0]
		}
		return out[i].InboundID < out[j].InboundID
	})
	return out
}

func orderedProxyPathSteps(steps []model.ProxyPathStep) []model.ProxyPathStep {
	out := append([]model.ProxyPathStep(nil), steps...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position == out[j].Position {
			return out[i].ID < out[j].ID
		}
		return out[i].Position < out[j].Position
	})
	return out
}

// ProxyPathAccountingServerID returns the server where the user protocol is
// first authenticated and decrypted. Downstream shared chain services and
// tunnels deliberately carry no billable end-user identity.
func ProxyPathAccountingServerID(path model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) (int64, bool) {
	rootServerID := int64(0)
	for _, inbound := range inbounds {
		if inbound.ID == path.InboundID && inbound.Enabled {
			rootServerID = inbound.ServerID
			break
		}
	}
	if !path.Enabled || rootServerID == 0 {
		return 0, false
	}
	ordered := orderedProxyPathSteps(steps)
	for _, step := range ordered {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode != model.ProxyPathTransportPortForward {
			break
		}
		if !step.ProcessingRole {
			continue
		}
		if step.ServerID != nil && *step.ServerID != 0 {
			return *step.ServerID, true
		}
		if step.InboundID != nil && *step.InboundID != 0 {
			for _, inbound := range inbounds {
				if inbound.ID == *step.InboundID {
					return inbound.ServerID, true
				}
			}
		}
	}
	return rootServerID, true
}

func TrafficAccountingUsersForServer(serverID int64, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, bindings []model.InboundUser, pathBindingSets ...[]model.ProxyPathUser) map[int64]bool {
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	pathsByInbound := map[int64][]model.ProxyPath{}
	for _, path := range paths {
		if path.Enabled {
			pathsByInbound[path.InboundID] = append(pathsByInbound[path.InboundID], path)
		}
	}
	accountingByInbound := map[int64]bool{}
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		inboundPaths := pathsByInbound[inbound.ID]
		if len(inboundPaths) == 0 {
			accountingByInbound[inbound.ID] = inbound.ServerID == serverID
			continue
		}
		for _, path := range inboundPaths {
			accountingServerID, ok := ProxyPathAccountingServerID(path, stepsByPath[path.ID], inbounds)
			if ok && accountingServerID == serverID {
				accountingByInbound[inbound.ID] = true
				break
			}
		}
	}
	users := map[int64]bool{}
	if len(pathBindingSets) > 0 {
		accountingByPath := map[int64]bool{}
		for _, path := range paths {
			accountingServerID, ok := ProxyPathAccountingServerID(path, stepsByPath[path.ID], inbounds)
			accountingByPath[path.ID] = ok && accountingServerID == serverID
		}
		for _, binding := range pathBindingSets[0] {
			if binding.Enabled && accountingByPath[binding.ProxyPathID] {
				users[binding.UserID] = true
			}
		}
		for _, binding := range bindings {
			if binding.Enabled && accountingByInbound[binding.InboundID] && len(pathsByInbound[binding.InboundID]) == 0 {
				users[binding.UserID] = true
			}
		}
		return users
	}
	for _, binding := range bindings {
		if binding.Enabled && accountingByInbound[binding.InboundID] {
			users[binding.UserID] = true
		}
	}
	return users
}

// TrafficAccountingServerIDsForUser returns the servers that own billing and
// protocol authentication for userID. Downstream shared SS, SSH tunnels, and
// WARP hops are excluded because they never decrypt the end-user protocol.
func TrafficAccountingServerIDsForUser(userID int64, servers []model.Server, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, bindings []model.InboundUser, pathBindingSets ...[]model.ProxyPathUser) []int64 {
	if userID <= 0 {
		return nil
	}
	out := make([]int64, 0)
	for _, server := range servers {
		if server.ID <= 0 {
			continue
		}
		users := TrafficAccountingUsersForServer(server.ID, paths, steps, inbounds, bindings, pathBindingSets...)
		if users[userID] {
			out = append(out, server.ID)
		}
	}
	return out
}

func IsProxyPathAccountingLocation(serverID, inboundID, pathID int64, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) bool {
	var selected model.ProxyPath
	found := false
	for _, path := range paths {
		if path.ID == pathID && path.InboundID == inboundID && path.Enabled {
			selected = path
			found = true
			break
		}
	}
	if !found {
		return false
	}
	pathSteps := make([]model.ProxyPathStep, 0)
	for _, step := range steps {
		if step.PathID == pathID {
			pathSteps = append(pathSteps, step)
		}
	}
	accountingServerID, ok := ProxyPathAccountingServerID(selected, pathSteps, inbounds)
	return ok && accountingServerID == serverID
}

func TrustedForwardServerIDs(paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) map[int64]bool {
	inboundByID := make(map[int64]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	required := map[int64]bool{}
	for _, path := range paths {
		root, ok := inboundByID[path.InboundID]
		if !path.Enabled || !ok || !root.Enabled {
			continue
		}
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		usesTrustedForward := false
		pathServers := map[int64]bool{root.ServerID: true}
		for _, step := range ordered {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if mode != model.ProxyPathTransportPortForward {
				break
			}
			if step.ServerID != nil && *step.ServerID > 0 {
				pathServers[*step.ServerID] = true
			} else if step.InboundID != nil {
				if inbound, exists := inboundByID[*step.InboundID]; exists {
					pathServers[inbound.ServerID] = true
				}
			}
			if step.ProcessingRole {
				usesTrustedForward = true
				break
			}
		}
		if usesTrustedForward {
			for id := range pathServers {
				required[id] = true
			}
		}
	}
	return required
}

// ProxyPathRequiresAccountingPathID reports whether traffic for an inbound can
// only be accounted on a downstream transparent-processing server. In that
// case an Agent report must identify the path; accepting a path-less report
// from the root server would allow the same user bytes to be counted twice.
func ProxyPathRequiresAccountingPathID(inboundID int64, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) bool {
	rootServerID := int64(0)
	for _, inbound := range inbounds {
		if inbound.ID == inboundID && inbound.Enabled {
			rootServerID = inbound.ServerID
			break
		}
	}
	if rootServerID == 0 {
		return false
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	for _, path := range paths {
		if !path.Enabled || path.InboundID != inboundID {
			continue
		}
		accountingServerID, ok := ProxyPathAccountingServerID(path, stepsByPath[path.ID], inbounds)
		if ok && accountingServerID != rootServerID {
			return true
		}
	}
	return false
}

// validateProxyPathTransportSemantics returns true when the user protocol must
// remain encrypted until a downstream processing server.
func validateProxyPathTransportSemantics(path model.ProxyPath, root model.Inbound, steps []model.ProxyPathStep) (bool, error) {
	if root.Protocol == model.ProtocolSSH {
		for _, step := range steps {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if mode == model.ProxyPathTransportPortForward {
				return false, fmt.Errorf("代理路径 %s 的 SSH 入口已在 Agent 解密，不能使用透明端口转发", path.Name)
			}
		}
	}
	processingIndex := -1
	for index, step := range steps {
		if !step.ProcessingRole {
			continue
		}
		if processingIndex >= 0 {
			return false, fmt.Errorf("代理路径 %s 只能设置一个处理加解密节点", path.Name)
		}
		if step.NodeType != model.ProxyPathStepServerInbound {
			return false, fmt.Errorf("代理路径 %s 的处理加解密节点必须是可控服务器", path.Name)
		}
		if step.InboundID != nil && *step.InboundID != 0 {
			return false, fmt.Errorf("代理路径 %s 的处理加解密节点必须选择服务器，由系统创建隐藏处理入口，不能复用已有入口", path.Name)
		}
		processingIndex = index
	}
	if processingIndex < 0 {
		for _, step := range steps {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if mode == model.ProxyPathTransportPortForward {
				return false, fmt.Errorf("代理路径 %s 使用%s前必须设置后端处理加解密服务器", path.Name, proxyPathTransportName(mode))
			}
		}
		return false, nil
	}
	if root.Protocol == model.ProtocolMieru {
		ports, err := MieruInboundPorts(root)
		if err != nil {
			return false, fmt.Errorf("代理路径 %s 的 Mieru 入口端口无效：%w", path.Name, err)
		}
		if len(ports) > 1 {
			return false, fmt.Errorf("代理路径 %s 的多端口 Mieru 入口不能使用可信透明转发", path.Name)
		}
	}
	for index, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if index <= processingIndex {
			if mode == model.ProxyPathTransportTunnel {
				return false, fmt.Errorf("代理路径 %s 的隧道尚未接入透明用户入口数据面；请先使用端口转发", path.Name)
			}
			if mode != model.ProxyPathTransportPortForward {
				return false, fmt.Errorf("代理路径 %s 在处理节点之前必须使用端口转发透明传递，不能提前由 sing-box 解密", path.Name)
			}
			continue
		}
		if mode == model.ProxyPathTransportPortForward {
			return false, fmt.Errorf("代理路径 %s 在透明转发结束后不能再次使用端口转发", path.Name)
		}
	}
	if root.Port <= 0 {
		return false, fmt.Errorf("代理路径 %s 的入口端口无效", path.Name)
	}
	return true, nil
}

func proxyPathTransportName(mode model.ProxyPathStepTransportMode) string {
	switch mode {
	case model.ProxyPathTransportPortForward:
		return "端口转发"
	case model.ProxyPathTransportTunnel:
		return "隧道"
	default:
		return "sing-box 出站链"
	}
}

func inboundUsesTransparentProcessing(inboundID int64, paths []model.ProxyPath, steps []model.ProxyPathStep) bool {
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	for _, path := range paths {
		if !path.Enabled || path.InboundID != inboundID {
			continue
		}
		for _, step := range stepsByPath[path.ID] {
			if step.ProcessingRole {
				return true
			}
		}
	}
	return false
}

func transparentForwardProtocol(inbound model.Inbound) model.ForwardProtocol {
	switch inbound.Protocol {
	case model.ProtocolHY2:
		return model.ForwardProtocolUDP
	case model.ProtocolVLESS, model.ProtocolAnyTLS:
		return model.ForwardProtocolTCP
	case model.ProtocolSS, model.ProtocolSocks:
		network := strings.ToLower(strings.TrimSpace(stringValue(parseStepConfig(inbound.ConfigJSON), "network", "")))
		switch network {
		case "tcp":
			return model.ForwardProtocolTCP
		case "udp":
			return model.ForwardProtocolUDP
		default:
			return model.ForwardProtocolTCPUDP
		}
	case model.ProtocolMieru:
		if MieruInboundTransport(inbound) == "UDP" {
			return model.ForwardProtocolUDP
		}
		return model.ForwardProtocolTCP
	default:
		return model.ForwardProtocolTCP
	}
}

func proxyPathPlanTargetInbound(path model.ProxyPath, step model.ProxyPathStep, targetServerID int64, targetInbound *model.Inbound, servers map[int64]model.Server, inbounds map[int64]model.Inbound, services map[proxyPathChainServiceKey]*proxyPathChainService, transparentGroup *transparentProxyPathGroup, ledger *ProxyPathPortLedger) model.Inbound {
	if targetInbound != nil {
		return *targetInbound
	}
	if service, ok := proxyPathChainServiceForStep(services, step, targetServerID); ok {
		return service.Inbound
	}
	server := servers[targetServerID]
	if transparentGroup != nil && step.Position <= transparentGroup.PrefixLength {
		if planned, ok := inbounds[proxyPathSharedTransparentInboundID(path.InboundID, step.Position)]; ok {
			return planned
		}
		return proxyPathSharedTransparentInbound(path.InboundID, step, server, inbounds, ledger)
	}
	return proxyPathInternalInbound(path, step, server, inbounds, ledger)
}

func proxyPathRuntimeTargetNode(path model.ProxyPath, step model.ProxyPathStep, targetServerID int64, inbound model.Inbound, services map[proxyPathChainServiceKey]*proxyPathChainService, transparentGroup *transparentProxyPathGroup) model.ProxyPathRuntimeNode {
	if service, ok := proxyPathChainServiceForStep(services, step, targetServerID); ok {
		return proxyPathRuntimeNode(
			fmt.Sprintf("shared-chain:%d", inbound.ID),
			step.ID,
			"shared_chain_inbound",
			inbound,
			proxyPathRuntimeProfile(service.ChainConfig),
			true,
		)
	}
	if transparentGroup != nil && step.Position <= transparentGroup.PrefixLength {
		return proxyPathRuntimeNode(
			fmt.Sprintf("transparent:inbound:%d:step:%d", transparentGroup.InboundID, step.Position),
			step.ID,
			"shared_transparent_inbound",
			inbound,
			"",
			true,
		)
	}
	return proxyPathRuntimeNode(
		fmt.Sprintf("path-internal:path:%d:step:%d", path.ID, step.Position),
		step.ID,
		"path_internal_inbound",
		inbound,
		"",
		false,
	)
}

func proxyPathRuntimeNode(resourceKey string, stepID int64, kind string, inbound model.Inbound, profile string, shared bool) model.ProxyPathRuntimeNode {
	listenScope := "public"
	if inbound.ListenIP == "127.0.0.1" || inbound.ListenIP == "::1" || strings.EqualFold(inbound.ListenIP, "localhost") {
		listenScope = "loopback"
	}
	return model.ProxyPathRuntimeNode{
		ResourceKey: resourceKey,
		StepID:      stepID,
		Kind:        kind,
		Name:        inbound.Name,
		ServerID:    inbound.ServerID,
		Protocol:    inbound.Protocol,
		Profile:     profile,
		ListenIP:    inbound.ListenIP,
		Port:        inbound.Port,
		Network:     proxyPathRuntimeNetwork(inbound),
		ListenScope: listenScope,
		Shared:      shared,
	}
}

func proxyPathRuntimeProfile(config ProxyPathChainConfig) string {
	switch config.Protocol {
	case model.ProtocolSS:
		return config.Method
	case model.ProtocolVLESS:
		return fmt.Sprintf("Reality %s:%d", config.RealityHandshakeServer, config.RealityHandshakePort)
	case model.ProtocolMieru:
		return "TCP"
	case model.ProtocolSocks:
		return "SOCKS5"
	default:
		return ""
	}
}

func proxyPathRuntimeNetwork(inbound model.Inbound) model.ForwardProtocol {
	switch inbound.Protocol {
	case model.ProtocolHY2:
		return model.ForwardProtocolUDP
	case model.ProtocolSS, model.ProtocolSocks:
		return transparentForwardProtocol(inbound)
	case model.ProtocolMieru:
		if MieruInboundTransport(inbound) == "UDP" {
			return model.ForwardProtocolUDP
		}
		return model.ForwardProtocolTCP
	default:
		return model.ForwardProtocolTCP
	}
}

func finalizeProxyPathRuntimeNodeReferences(plans []model.ProxyPathPlan) {
	references := map[string]map[int64]bool{}
	for planIndex := range plans {
		seen := map[string]bool{}
		nodes := make([]model.ProxyPathRuntimeNode, 0, len(plans[planIndex].RuntimeNodes))
		for _, node := range plans[planIndex].RuntimeNodes {
			if node.ResourceKey == "" || seen[node.ResourceKey] {
				continue
			}
			seen[node.ResourceKey] = true
			nodes = append(nodes, node)
			if references[node.ResourceKey] == nil {
				references[node.ResourceKey] = map[int64]bool{}
			}
			references[node.ResourceKey][plans[planIndex].PathID] = true
		}
		plans[planIndex].RuntimeNodes = nodes
	}
	for planIndex := range plans {
		for nodeIndex := range plans[planIndex].RuntimeNodes {
			node := &plans[planIndex].RuntimeNodes[nodeIndex]
			node.ReferenceCount = len(references[node.ResourceKey])
			node.Shared = node.ReferenceCount > 1
		}
	}
}

func DerivedPortForwardsFromProxyPaths(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound) ([]model.PortForward, error) {
	return DerivedPortForwardsFromProxyPathsWithLedger(paths, steps, servers, inbounds, nil)
}

func DerivedPortForwardsFromProxyPathsWithLedger(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.PortForward, error) {
	plans, err := BuildProxyPathPlansWithLedger(paths, steps, servers, inbounds, ledger)
	if err != nil {
		return nil, err
	}
	out := []model.PortForward{}
	seen := map[int64]model.PortForward{}
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		for _, forward := range plan.PortForwards {
			if previous, ok := seen[forward.ID]; ok {
				if previous.SourceServerID != forward.SourceServerID || previous.TargetServerID != forward.TargetServerID || previous.ListenIP != forward.ListenIP || previous.ListenPort != forward.ListenPort || previous.TargetAddress != forward.TargetAddress || previous.TargetPort != forward.TargetPort || previous.Protocol != forward.Protocol || previous.Backend != forward.Backend || !sameTrustedForwardSender(previous.TrustedForward, forward.TrustedForward) {
					return nil, fmt.Errorf("共享透明转发资源 %d 的投影不一致", forward.ID)
				}
				continue
			}
			seen[forward.ID] = forward
			out = append(out, forward)
		}
	}
	return out, nil
}

func sameTrustedForwardSender(left, right *model.TrustedForwardSender) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func DerivedTunnelsFromProxyPaths(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound) ([]model.Tunnel, error) {
	return DerivedTunnelsFromProxyPathsWithLedger(paths, steps, servers, inbounds, nil)
}

func DerivedTunnelsFromProxyPathsWithLedger(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.Tunnel, error) {
	plans, err := BuildProxyPathPlansWithLedger(paths, steps, servers, inbounds, ledger)
	if err != nil {
		return nil, err
	}
	out := []model.Tunnel{}
	seen := map[int64]bool{}
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		for _, tunnel := range plan.Tunnels {
			if seen[tunnel.ID] {
				continue
			}
			seen[tunnel.ID] = true
			out = append(out, tunnel)
		}
	}
	return out, nil
}

func proxyPathStepTargetServer(step model.ProxyPathStep, inboundByID map[int64]model.Inbound) (int64, *model.Inbound, bool) {
	if step.InboundID != nil && *step.InboundID != 0 {
		inbound, ok := inboundByID[*step.InboundID]
		if !ok {
			return 0, nil, false
		}
		return inbound.ServerID, &inbound, true
	}
	if step.ServerID != nil && *step.ServerID != 0 {
		return *step.ServerID, nil, true
	}
	return 0, nil, false
}

func proxyPathManagedPortForward(path model.ProxyPath, step model.ProxyPathStep, root model.Inbound, sourceServerID, targetServerID int64, listenPort int, targetInbound model.Inbound, servers map[int64]model.Server, transparentGroup *transparentProxyPathGroup) (model.PortForward, error) {
	if sourceServerID == 0 || targetServerID == 0 || sourceServerID == targetServerID {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳端口转发的源/目标服务器无效", path.Name, step.Position)
	}
	target, ok := servers[targetServerID]
	if !ok {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳目标服务器不存在", path.Name, step.Position)
	}
	if listenPort <= 0 {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳端口转发监听端口无效", path.Name, step.Position)
	}
	source, ok := servers[sourceServerID]
	if !ok {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳源服务器不存在", path.Name, step.Position)
	}
	backend := model.ForwardBackendAuto
	if v := stringValue(parseStepConfig(step.ConfigJSON), "backend", ""); v != "" {
		backend = model.ForwardBackend(v)
	}
	protocol := transparentForwardProtocol(root)
	targetAddress, err := proxyPathReachableServerAddress(source, target)
	if err != nil {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳: %w", path.Name, step.Position, err)
	}
	id := syntheticProxyPathID(path.ID, step.ID, 10)
	name := fmt.Sprintf("%s / 第%d跳", firstNonEmpty(path.Name, "代理路径"), step.Position)
	configJSON := managedConfigJSON(path.ID, step.ID)
	if transparentGroup != nil {
		id = stableProxyPathResourceID("proxy-path-transparent-forward", transparentGroup.InboundID, step.Position)
		name = fmt.Sprintf("%s / 透明第%d跳", firstNonEmpty(root.Name, fmt.Sprintf("入口 %d", root.ID)), step.Position)
		configJSON = managedTransparentConfigJSON(transparentGroup.InboundID, step.Position)
	}
	return model.PortForward{ID: id, Name: name, SourceServerID: sourceServerID, TargetServerID: targetServerID, ListenIP: EffectiveListenIP(source, firstNonEmpty(stringValue(parseStepConfig(step.ConfigJSON), "listen_ip", ""), source.ListenIP)), ListenPort: listenPort, TargetAddress: targetAddress, TargetPort: targetInbound.Port, Protocol: protocol, Backend: backend, ProbeMode: "apply", ProbeIntervalSeconds: 300, Priority: 1000 + step.Position, ConfigJSON: configJSON, Enabled: true}, nil
}

func proxyPathReachableServerAddress(source, target model.Server) (string, error) {
	return ResolveReachableServerEntryAddress(source, target)
}

func proxyPathManagedTunnel(path model.ProxyPath, step model.ProxyPathStep, sourceServerID, targetServerID int64, targetInbound model.Inbound, servers map[int64]model.Server, inbounds map[int64]model.Inbound, ledger *ProxyPathPortLedger) (model.Tunnel, error) {
	if sourceServerID == 0 || targetServerID == 0 || sourceServerID == targetServerID {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳隧道的源/目标服务器无效", path.Name, step.Position)
	}
	source, sourceOK := servers[sourceServerID]
	target, ok := servers[targetServerID]
	if !sourceOK {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳源服务器不存在", path.Name, step.Position)
	}
	if !ok {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳目标服务器不存在", path.Name, step.Position)
	}
	targetEndpoint, err := ResolveReachableServerEntryAddress(source, target)
	if err != nil {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳: %w", path.Name, step.Position, err)
	}
	cfg := parseStepConfig(step.ConfigJSON)
	typeName := model.TunnelType(strings.ToLower(stringValue(cfg, "type", string(model.TunnelTypeSSH))))
	reuseKey := proxyPathTunnelReuseKey(step, sourceServerID, targetServerID, targetInbound)
	tunnel := model.Tunnel{ID: stableProxyPathResourceID("proxy-path-tunnel", reuseKey), Name: fmt.Sprintf("共享隧道 / %s -> %s", source.Name, target.Name), SourceServerID: sourceServerID, TargetServerID: targetServerID, Type: typeName, Priority: 1000, Enabled: true}
	switch typeName {
	case model.TunnelTypeSSH:
		clientPrivateKey, clientPublicKey, err := proxyPathSSHKeyPair(source, target, reuseKey)
		if err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳生成共享 SSH 凭据: %w", path.Name, step.Position, err)
		}
		listenPort := ledger.resolve(PortRequirement{
			Kind:           model.ProxyPathPortKindTunnelSSH,
			ScopeKey:       fmt.Sprint(tunnel.ID),
			ServerID:       source.ID,
			Pool:           model.PortPoolInternal,
			ListenIP:       "127.0.0.1",
			Network:        model.ForwardProtocolTCP,
			PolicyRevision: serverPortPolicyRevision(source),
			Allocate: func() int {
				return proxyPathTunnelPort(source, tunnel.ID, 0, 31, inbounds)
			},
		})
		sshPort := intValueFromMap(cfg, "ssh_port", 0)
		if sshPort == 0 {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳未设置目标端隧道服务端口", path.Name, step.Position)
		}
		if err := ValidatePort(sshPort); err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳目标端隧道服务端口: %w", path.Name, step.Position, err)
		}
		if !proxyPathPortAvailable(target.ID, sshPort, model.ForwardProtocolTCP, inbounds) {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳目标端隧道服务端口 %d 已被目标服务器的 TCP 服务占用", path.Name, step.Position, sshPort)
		}
		sshConfig := map[string]any{
			"managed_pair":       true,
			"client_private_key": clientPrivateKey,
			"client_public_key":  clientPublicKey,
			"user":               "oboard_tunnel",
			"local_forward":      fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", listenPort, targetInbound.Port),
			"permit_open":        fmt.Sprintf("127.0.0.1:%d", targetInbound.Port),
			"managed_by":         "proxy_path_shared",
		}
		b, _ := json.Marshal(sshConfig)
		tunnel.ListenPort = listenPort
		tunnel.TargetEndpoint = targetEndpoint
		tunnel.TargetPort = sshPort
		tunnel.ConfigJSON = string(b)
	case model.TunnelTypeWireGuard:
		sourcePrivateKey, sourcePublicKey, err := proxyPathWireGuardKeyPair(source, target, reuseKey, "wireguard-source")
		if err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳生成共享 WireGuard 源凭据: %w", path.Name, step.Position, err)
		}
		targetPrivateKey, targetPublicKey, err := proxyPathWireGuardKeyPair(source, target, reuseKey, "wireguard-target")
		if err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳生成共享 WireGuard 目标凭据: %w", path.Name, step.Position, err)
		}
		sourceAddress, targetAddress := proxyPathWireGuardAddresses(tunnel.ID, 0)
		listenPort := ledger.resolve(PortRequirement{
			Kind:           model.ProxyPathPortKindTunnelWG,
			ScopeKey:       fmt.Sprint(tunnel.ID),
			ServerID:       target.ID,
			Pool:           model.PortPoolPublic,
			ListenIP:       "*",
			Network:        model.ForwardProtocolUDP,
			PolicyRevision: serverPortPolicyRevision(target),
			Allocate: func() int {
				return proxyPathTunnelPort(target, tunnel.ID, 0, 47, inbounds)
			},
		})
		if listenPort == 0 {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳的目标服务器管理公网端口范围内没有可用 WireGuard UDP 端口", path.Name, step.Position)
		}
		pair := map[string]any{
			"managed_pair":         true,
			"source_address":       sourceAddress,
			"target_address":       targetAddress,
			"source_private_key":   sourcePrivateKey,
			"source_public_key":    sourcePublicKey,
			"target_private_key":   targetPrivateKey,
			"target_public_key":    targetPublicKey,
			"persistent_keepalive": intValueFromMap(cfg, "persistent_keepalive", 25),
			"managed_by":           "proxy_path_shared",
		}
		b, _ := json.Marshal(pair)
		tunnel.LocalAddress = sourceAddress
		tunnel.PeerAddress = prefixHost(targetAddress) + "/32"
		tunnel.TargetEndpoint = targetEndpoint
		tunnel.TargetPort = listenPort
		tunnel.ConfigJSON = string(b)
	default:
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳隧道类型必须是 ssh 或 wireguard", path.Name, step.Position)
	}
	return tunnel, nil
}

func proxyPathTunnelReuseKey(step model.ProxyPathStep, sourceServerID, targetServerID int64, targetInbound model.Inbound) string {
	cfg := parseStepConfig(step.ConfigJSON)
	typeName := strings.ToLower(stringValue(cfg, "type", string(model.TunnelTypeSSH)))
	switch model.TunnelType(typeName) {
	case model.TunnelTypeSSH:
		sshPort := intValueFromMap(cfg, "ssh_port", 0)
		return fmt.Sprintf("ssh:%d:%d:%d:%d", sourceServerID, targetServerID, sshPort, targetInbound.Port)
	case model.TunnelTypeWireGuard:
		// persistent_keepalive is a tuning value, not part of the peer identity.
		// Including it would rotate the whole key pair and the interface addresses
		// when an operator only adjusts the keepalive interval.
		return fmt.Sprintf("wireguard:%d:%d", sourceServerID, targetServerID)
	default:
		return fmt.Sprintf("%s:%d:%d", typeName, sourceServerID, targetServerID)
	}
}

func proxyPathTunnelDialTarget(tunnel model.Tunnel) (string, int, error) {
	switch tunnel.Type {
	case model.TunnelTypeSSH:
		return "127.0.0.1", tunnel.ListenPort, nil
	case model.TunnelTypeWireGuard:
		return prefixHost(tunnel.PeerAddress), 0, nil
	default:
		return "", 0, errors.New("隧道类型必须是 ssh 或 wireguard")
	}
}

func proxyPathTunnelPort(server model.Server, pathID int64, position, salt int, inbounds map[int64]model.Inbound) int {
	// SSH local forwarding listens only on loopback. Keeping it in the internal
	// pool prevents a one-port server range from colliding with the public
	// inbound that already owns that port, and provider port-range changes never
	// touch it.
	if salt == 31 {
		start, end := proxyPathInternalPortRange(server)
		return proxyPathAvailablePort(server, pathID*193, position*71+salt, start, end, "127.0.0.1", inbounds)
	}
	start, end := proxyPathServerPortRange(server)
	protocol := model.ForwardProtocolTCP
	listenIP := EffectiveListenIP(server, server.ListenIP)
	if salt == 47 {
		protocol = model.ForwardProtocolUDP
		listenIP = "*"
	}
	return proxyPathAvailablePortForProtocol(server, pathID*193, position*71+salt, start, end, protocol, listenIP, inbounds)
}

func proxyPathAvailablePort(server model.Server, pathSeed int64, positionSeed, start, end int, listenIP string, inbounds map[int64]model.Inbound) int {
	return proxyPathAvailablePortForProtocol(server, pathSeed, positionSeed, start, end, "", listenIP, inbounds)
}

// proxyPathAvailablePortForProtocol finds a free port inside [start, end] using
// the stable seed and the same listen-resource conflict model as deployment
// validation: address scope, TCP/UDP transport and port all participate, so two
// listeners may share a numeric port when they bind different concrete
// addresses or use non-overlapping transports, and a wildcard address conflicts
// with everything. Exhaustion returns 0: managed public listeners must never
// silently fall back outside the configured auto range — a port that cannot be
// reached through the provider's NAT or firewall is useless even if the bind
// would succeed.
func proxyPathAvailablePortForProtocol(server model.Server, pathSeed int64, positionSeed, start, end int, protocol model.ForwardProtocol, listenIP string, inbounds map[int64]model.Inbound) int {
	reserved := make([]listenResource, 0, len(inbounds))
	for _, inbound := range inbounds {
		// Disabled inbounds are reserved too. Allocation is deterministic, so
		// handing a disabled inbound's port to a generated listener would create a
		// listener conflict the operator cannot resolve by re-enabling it.
		if inbound.ServerID == server.ID && inbound.Port > 0 {
			reserved = append(reserved, inboundListenResource(inbound))
		}
	}
	// The candidate carries the address the listener will actually bind:
	// loopback for SSH forwards, wildcard for WireGuard, and the server listen
	// address elsewhere. Using the wrong scope would let allocation pick a port
	// that deployment validation later rejects (or skip one it could reuse).
	candidate := listenResource{
		serverID: server.ID,
		address:  normalizeListenAddress(listenIP),
		protocol: listenTransportForAllocation(protocol),
	}
	span := end - start + 1
	if span <= 0 {
		return 0
	}
	seed := int((pathSeed + int64(positionSeed)) % int64(span))
	if seed < 0 {
		seed = -seed
	}
	for offset := 0; offset < span; offset++ {
		candidate.port = start + (seed+offset)%span
		free := true
		for _, existing := range reserved {
			if candidate.conflicts(existing) {
				free = false
				break
			}
		}
		if free {
			return candidate.port
		}
	}
	return 0
}

// listenTransportForAllocation maps a requested network to the transport bits
// allocation checks. An empty network means a TCP listener in every current
// call site; it deliberately does not expand to "both transports", which would
// wrongly block UDP-only inbounds (for example Hysteria2) from sharing the
// numeric port with a TCP listener.
func listenTransportForAllocation(protocol model.ForwardProtocol) listenTransport {
	if protocol == "" {
		return listenTCP
	}
	return portForwardListenTransport(protocol)
}

func proxyPathPortAvailable(serverID int64, port int, protocol model.ForwardProtocol, inbounds map[int64]model.Inbound) bool {
	for _, inbound := range inbounds {
		if inbound.ServerID != serverID || inbound.Port != port {
			continue
		}
		if proxyPathInboundUsesProtocol(inbound, protocol) {
			return false
		}
	}
	return true
}

func proxyPathInboundUsesProtocol(inbound model.Inbound, protocol model.ForwardProtocol) bool {
	inboundProtocol := transparentForwardProtocol(inbound)
	return inboundProtocol == model.ForwardProtocolTCPUDP || inboundProtocol == protocol
}

func proxyPathServerPortRange(server model.Server) (int, int) {
	if server.PortRangeStart > 0 && server.PortRangeEnd >= server.PortRangeStart {
		return server.PortRangeStart, server.PortRangeEnd
	}
	return DefaultPublicPortRangeStart, DefaultPublicPortRangeEnd
}

// proxyPathInternalPortRange returns the loopback-only allocation pool. The
// internal pool is not constrained by provider NAT or firewall port ranges, so
// changing the public auto range never migrates these ports.
func proxyPathInternalPortRange(server model.Server) (int, int) {
	if server.InternalPortRangeStart > 0 && server.InternalPortRangeEnd >= server.InternalPortRangeStart {
		return server.InternalPortRangeStart, server.InternalPortRangeEnd
	}
	return DefaultInternalPortRangeStart, DefaultInternalPortRangeEnd
}

// serverPortPolicyRevision returns the active port policy revision of a server.
// Rows created before the field existed normalize to revision 1, the first
// policy generation. New allocations and migration generations carry this
// revision so a migration can prove which policy an owner was built for.
func serverPortPolicyRevision(server model.Server) int64 {
	if server.PortPolicyRevision <= 0 {
		return 1
	}
	return server.PortPolicyRevision
}

// PortAllocationPoolForKind classifies legacy allocation rows whose pool column
// predates the pool metadata. Loopback-only listeners (trusted forward inner
// receivers, SSH -L forwards) belong to the internal pool no matter where an old
// allocator happened to pick their port.
func PortAllocationPoolForKind(kind string) string {
	switch kind {
	case model.ProxyPathPortKindTrustedInner, model.ProxyPathPortKindTunnelSSH:
		return model.PortPoolInternal
	default:
		return model.PortPoolPublic
	}
}

func reserveProxyPathTunnelPorts(tunnel model.Tunnel, inbounds map[int64]model.Inbound) {
	if tunnel.TargetServerID == 0 || tunnel.TargetPort <= 0 {
		return
	}
	if tunnel.Type == model.TunnelTypeWireGuard {
		id := -tunnel.ID
		inbounds[id] = model.Inbound{ID: id, ServerID: tunnel.TargetServerID, Protocol: model.ProtocolHY2, Port: tunnel.TargetPort, Enabled: true}
	}
	if tunnel.Type == model.TunnelTypeSSH && tunnel.SourceServerID != 0 && tunnel.ListenPort > 0 {
		localID := -tunnel.ID - 1
		inbounds[localID] = model.Inbound{ID: localID, ServerID: tunnel.SourceServerID, Protocol: model.ProtocolVLESS, ListenIP: "127.0.0.1", Port: tunnel.ListenPort, Enabled: true}
	}
}

func proxyPathWireGuardAddresses(pathID int64, position int) (string, string) {
	network := (pathID*4099 + int64(position)*131) & 0x3ffff
	second := 16 + (network >> 16)
	third := (network >> 8) & 0xff
	fourth := (network & 0xff) & 0xfc
	return fmt.Sprintf("172.%d.%d.%d/30", second, third, fourth+1), fmt.Sprintf("172.%d.%d.%d/30", second, third, fourth+2)
}

func prefixHost(value string) string {
	if index := strings.IndexByte(value, '/'); index >= 0 {
		return value[:index]
	}
	return value
}

func parseStepConfig(raw string) map[string]any {
	var m map[string]any
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func intValueFromMap(m map[string]any, key string, fallback int) int {
	switch v := m[key].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

const (
	// syntheticProxyPathIDBase keeps derived IDs far away from autoincrement rows
	// and from the negative ranges used by generated inbounds.
	syntheticProxyPathIDBase  = int64(1) << 52
	syntheticProxyPathIDKind  = int64(1) << 48
	syntheticProxyPathIDShift = 24
	syntheticProxyPathIDField = int64(1)<<syntheticProxyPathIDShift - 1
)

// syntheticProxyPathID derives a stable resource ID for a component owned by one
// path step. Path and step IDs occupy disjoint bit fields: step IDs come from a
// global autoincrement, so a decimal layout would let a step ID above its field
// width carry into the neighbouring path's range and silently share one derived
// resource between two paths. The result stays below 2^53 so it survives the
// JSON round-trip through the Web UI without losing precision.
func syntheticProxyPathID(pathID, stepID int64, kind int64) int64 {
	return syntheticProxyPathIDBase +
		kind*syntheticProxyPathIDKind +
		(pathID&syntheticProxyPathIDField)<<syntheticProxyPathIDShift +
		(stepID & syntheticProxyPathIDField)
}

func managedConfigJSON(pathID, stepID int64) string {
	b, _ := json.Marshal(map[string]any{"managed_by": "proxy_path", "path_id": pathID, "step_id": stepID})
	return string(b)
}

func managedTransparentConfigJSON(inboundID int64, position int) string {
	b, _ := json.Marshal(map[string]any{"managed_by": "proxy_path_transparent_prefix", "inbound_id": inboundID, "position": position})
	return string(b)
}
