package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const DefaultProxyPathChainMethod = "2022-blake3-aes-128-gcm"

var proxyPathChainMethods = map[string]int{
	"2022-blake3-aes-128-gcm":       1,
	"2022-blake3-aes-256-gcm":       2,
	"2022-blake3-chacha20-poly1305": 3,
}

// ProxyPathPortLedger holds the persisted generated-listener ports and records
// the ones a projection had to allocate. A projection prefers a stored port so
// that an unrelated change — a new inbound, a disabled branch, another path
// claiming a nearby port — cannot move a listener that is already deployed.
//
// The ledger is read-only with respect to the database. Controller persists
// Pending() after a projection succeeds, so a rejected deployment leaves no
// half-claimed ports behind.
type ProxyPathPortLedger struct {
	stored   map[proxyPathPortKey]int
	pending  map[proxyPathPortKey]int
	used     map[proxyPathPortKey]bool
	order    []proxyPathPortKey
	complete bool
}

type proxyPathPortKey struct {
	Kind     string
	ScopeKey string
	ServerID int64
}

// NewProxyPathPortLedger builds a ledger from persisted allocations. A nil or
// empty slice yields a ledger that allocates everything fresh, which is also the
// behavior pure-Core callers and fixtures get.
func NewProxyPathPortLedger(allocations []model.ProxyPathPortAllocation) *ProxyPathPortLedger {
	ledger := &ProxyPathPortLedger{stored: make(map[proxyPathPortKey]int, len(allocations)), pending: map[proxyPathPortKey]int{}}
	for _, item := range allocations {
		if item.Port > 0 {
			ledger.stored[proxyPathPortKey{Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID}] = item.Port
		}
	}
	return ledger
}

// resolve returns the port for one listener, allocating through allocate only
// when neither a stored nor an already-pending value exists.
func (l *ProxyPathPortLedger) resolve(kind, scopeKey string, serverID int64, allocate func() int) int {
	if l == nil {
		return allocate()
	}
	key := proxyPathPortKey{Kind: kind, ScopeKey: scopeKey, ServerID: serverID}
	if port, ok := l.stored[key]; ok {
		if l.used == nil {
			l.used = map[proxyPathPortKey]bool{}
		}
		l.used[key] = true
		return port
	}
	if port, ok := l.pending[key]; ok {
		return port
	}
	port := allocate()
	if port <= 0 {
		return 0
	}
	l.pending[key] = port
	l.order = append(l.order, key)
	return port
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
// Releasing requires a complete projection. A run that aborted partway resolved
// only some listeners, and treating that partial view as authoritative would drop
// allocations that are still deployed.
func StaleProxyPathPortAllocationIDs(stored []model.ProxyPathPortAllocation, ledger *ProxyPathPortLedger) []int64 {
	if ledger == nil || !ledger.complete {
		return nil
	}
	out := []int64{}
	for _, item := range stored {
		key := proxyPathPortKey{Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID}
		if ledger.used[key] || ledger.pending[key] > 0 {
			continue
		}
		out = append(out, item.ID)
	}
	return out
}

// Pending returns the allocations this projection created, in allocation order.
func (l *ProxyPathPortLedger) Pending() []model.ProxyPathPortAllocation {
	if l == nil {
		return nil
	}
	out := make([]model.ProxyPathPortAllocation, 0, len(l.order))
	for _, key := range l.order {
		out = append(out, model.ProxyPathPortAllocation{Kind: key.Kind, ScopeKey: key.ScopeKey, ServerID: key.ServerID, Port: l.pending[key]})
	}
	return out
}

type proxyPathChainServiceKey struct {
	ServerID int64
	Method   string
}

type proxyPathChainService struct {
	Key      proxyPathChainServiceKey
	Inbound  model.Inbound
	Tag      string
	Password string
	Users    []model.User
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
		method := proxyPathStepChainMethod(step)
		if err := ValidateProxyPathChainMethod(method); err != nil {
			return nil, fmt.Errorf("proxy path %s step %d: %w", path.Name, step.Position, err)
		}
		server, ok := serverByID[*step.ServerID]
		if !ok {
			return nil, fmt.Errorf("proxy path %s step %d: target server not found", path.Name, step.Position)
		}
		key := proxyPathChainServiceKey{ServerID: server.ID, Method: method}
		service := services[key]
		if service == nil {
			service = &proxyPathChainService{Key: key}
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
			return proxyPathChainMethods[keys[i].Method] < proxyPathChainMethods[keys[j].Method]
		}
		return keys[i].ServerID < keys[j].ServerID
	})
	for _, key := range keys {
		service := services[key]
		server := serverByID[key.ServerID]
		methodIndex := proxyPathChainMethods[key.Method]
		start, end := proxyPathServerPortRange(server)
		port := ledger.resolve(model.ProxyPathPortKindChainService, key.Method, server.ID, func() int {
			return proxyPathAvailablePortForProtocol(server, server.ID*977, methodIndex*131, start, end, model.ForwardProtocolTCPUDP, true, occupied)
		})
		if port == 0 {
			return nil, fmt.Errorf("server %s has no available port for shared Shadowsocks chain service", server.Name)
		}
		service.Tag = proxyPathChainServiceTag(key.Method)
		service.Password = proxyPathChainServicePassword(server, key.Method)
		configJSON, _ := json.Marshal(map[string]any{"method": key.Method, "password": service.Password})
		service.Inbound = model.Inbound{
			ID:         proxyPathChainServiceID(key.ServerID, key.Method),
			ServerID:   key.ServerID,
			Name:       fmt.Sprintf("共享链路 / %s", key.Method),
			Protocol:   model.ProtocolSS,
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
	service, ok := services[proxyPathChainServiceKey{ServerID: targetServerID, Method: proxyPathStepChainMethod(step)}]
	return service, ok
}

func proxyPathChainServiceTag(method string) string {
	return "oboard-chain-ss-" + strings.NewReplacer("2022-blake3-", "", "-gcm", "", "-poly1305", "").Replace(method) + "-in"
}

// proxyPathChainServiceID derives the negative ID of a shared Shadowsocks
// listener. Server ID and method index use disjoint bit fields, keeping this
// range distinct from proxyPathInternalOutboundID for every reachable server ID.
func proxyPathChainServiceID(serverID int64, method string) int64 {
	return -(int64(1)<<41 + (serverID&0xffffff)<<4 + int64(proxyPathChainMethods[normalizeProxyPathChainMethod(method)])&0xf)
}

func proxyPathChainServicePassword(server model.Server, method string) string {
	seed := proxyPathServerChainSeed(server)
	sum := sha256.Sum256([]byte("oboard-chain-ss:" + seed + ":" + normalizeProxyPathChainMethod(method)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
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
				plannedInbound = proxyPathPlanTargetInbound(path, step, targetServerID, targetInbound, serverByID, inboundByID, chainServices, ledger)
				if step.TransportMode == model.ProxyPathTransportPortForward && step.ProcessingRole {
					plannedInbound.Protocol = root.Protocol
					plannedInbound.ConfigJSON = root.ConfigJSON
				}
				// Only an enabled path reserves runtime ports. A disabled branch
				// deploys nothing, so letting it occupy a port would make enabling or
				// disabling it silently change the allocation of unrelated paths.
				if path.Enabled {
					inboundByID[plannedInbound.ID] = plannedInbound
				}
				if path.Enabled && mode == model.ProxyPathTransportSingBox {
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
				f, err := proxyPathManagedPortForward(path, step, root, previousServerID, targetServerID, sourceListenPort, plannedInbound, serverByID)
				if err != nil {
					if path.Enabled {
						return nil, nil, err
					}
					plan.Warnings = append(plan.Warnings, err.Error())
					continue
				}
				plan.PortForwards = append(plan.PortForwards, f)
				sourceListenPort = plannedInbound.Port
			case model.ProxyPathTransportTunnel:
				if !targetOK {
					if path.Enabled {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳隧道需要目标服务器", path.Name, step.Position)
					}
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("第 %d 跳隧道需要目标服务器", step.Position))
					continue
				}
				tunnelKey := proxyPathTunnelReuseKey(step, previousServerID, targetServerID, plannedInbound, serverByID)
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
				plan.PortForwards[0].TrustedForward = &model.TrustedForwardSender{
					Version:             1,
					ReceiverID:          proxyPathTrustedForwardReceiverID(path.ID, step.ID),
					Key:                 proxyPathTrustedForwardKey(entryServer, processingServer, path.ID, step.ID),
					MaxClockSkewSeconds: 120,
				}
				break
			}
		}
		out = append(out, plan)
	}
	ledger.markProjectionComplete()
	return out, inboundByID, nil
}

func validateProxyPathTransportSet(paths []model.ProxyPath, stepsByPath map[int64][]model.ProxyPathStep, inboundByID map[int64]model.Inbound) error {
	enabledByInbound := map[int64]int{}
	transparentByInbound := map[int64]int{}
	directSignatures := map[string]bool{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		root, ok := inboundByID[path.InboundID]
		if !ok {
			continue
		}
		enabledByInbound[path.InboundID]++
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
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
			if path.Kind != "" && path.Kind != model.ProxyPathKindChain {
				return fmt.Errorf("WARP 只能作为普通代理链路的出口")
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
			if directSignatures[signature] {
				return fmt.Errorf("入口 %d 已存在相同位置的直接出口分支", path.InboundID)
			}
			directSignatures[signature] = true
		}
		transparent, err := validateProxyPathTransportSemantics(path, root, ordered)
		if err != nil {
			return err
		}
		if transparent {
			transparentByInbound[path.InboundID]++
		}
	}
	for inboundID, count := range transparentByInbound {
		if count > 0 && enabledByInbound[inboundID] != 1 {
			return fmt.Errorf("入口 %d 包含透明端口转发时只能启用一条路径；同一入口端口不能同时绑定多条分支", inboundID)
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
		root, ok := inboundByID[path.InboundID]
		if !ok {
			return nil, fmt.Errorf("代理路径 %s 的入口不存在", path.Name)
		}
		currentServerID := root.ServerID
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

func TrafficAccountingUsersForServer(serverID int64, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, bindings []model.InboundUser) map[int64]bool {
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
	for _, binding := range bindings {
		if binding.Enabled && accountingByInbound[binding.InboundID] {
			users[binding.UserID] = true
		}
	}
	return users
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
	case model.ProtocolSS:
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

func proxyPathPlanTargetInbound(path model.ProxyPath, step model.ProxyPathStep, targetServerID int64, targetInbound *model.Inbound, servers map[int64]model.Server, inbounds map[int64]model.Inbound, services map[proxyPathChainServiceKey]*proxyPathChainService, ledger *ProxyPathPortLedger) model.Inbound {
	if targetInbound != nil {
		return *targetInbound
	}
	if service, ok := proxyPathChainServiceForStep(services, step, targetServerID); ok {
		return service.Inbound
	}
	server := servers[targetServerID]
	return proxyPathInternalInbound(path, step, server, inbounds, ledger)
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
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		out = append(out, plan.PortForwards...)
	}
	return out, nil
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

func proxyPathManagedPortForward(path model.ProxyPath, step model.ProxyPathStep, root model.Inbound, sourceServerID, targetServerID int64, listenPort int, targetInbound model.Inbound, servers map[int64]model.Server) (model.PortForward, error) {
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
	return model.PortForward{ID: syntheticProxyPathID(path.ID, step.ID, 10), Name: fmt.Sprintf("%s / 第%d跳", firstNonEmpty(path.Name, "代理路径"), step.Position), SourceServerID: sourceServerID, TargetServerID: targetServerID, ListenIP: firstNonEmpty(stringValue(parseStepConfig(step.ConfigJSON), "listen_ip", ""), source.ListenIP, "0.0.0.0"), ListenPort: listenPort, TargetAddress: targetAddress, TargetPort: targetInbound.Port, Protocol: protocol, Backend: backend, ProbeMode: "apply", ProbeIntervalSeconds: 300, Priority: 1000 + step.Position, ConfigJSON: managedConfigJSON(path.ID, step.ID), Enabled: true}, nil
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
	reuseKey := proxyPathTunnelReuseKey(step, sourceServerID, targetServerID, targetInbound, servers)
	tunnel := model.Tunnel{ID: stableProxyPathResourceID("proxy-path-tunnel", reuseKey), Name: fmt.Sprintf("共享隧道 / %s -> %s", source.Name, target.Name), SourceServerID: sourceServerID, TargetServerID: targetServerID, Type: typeName, Priority: 1000, Enabled: true}
	switch typeName {
	case model.TunnelTypeSSH:
		clientPrivateKey, clientPublicKey, err := proxyPathSSHKeyPair(source, target, reuseKey)
		if err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳生成共享 SSH 凭据: %w", path.Name, step.Position, err)
		}
		listenPort := ledger.resolve(model.ProxyPathPortKindTunnelSSH, fmt.Sprint(tunnel.ID), source.ID, func() int {
			return proxyPathTunnelPort(source, tunnel.ID, 0, 31, inbounds)
		})
		sshPort := intValueFromMap(cfg, "ssh_port", 0)
		if sshPort == 0 {
			sshPort = target.SSHPort
		}
		if sshPort == 0 {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳的目标服务器 %s 未设置 SSH 端口", path.Name, step.Position, target.Name)
		}
		if err := ValidatePort(sshPort); err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳 SSH 端口: %w", path.Name, step.Position, err)
		}
		if !proxyPathPortAvailable(target.ID, sshPort, model.ForwardProtocolTCP, inbounds) {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳 SSH 端口 %d 已被目标服务器的 TCP 服务占用", path.Name, step.Position, sshPort)
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
		listenPort := ledger.resolve(model.ProxyPathPortKindTunnelWG, fmt.Sprint(tunnel.ID), target.ID, func() int {
			return proxyPathTunnelPort(target, tunnel.ID, 0, 47, inbounds)
		})
		if listenPort == 0 {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳的目标服务器端口范围内没有可用 WireGuard UDP 端口", path.Name, step.Position)
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

func proxyPathTunnelReuseKey(step model.ProxyPathStep, sourceServerID, targetServerID int64, targetInbound model.Inbound, servers map[int64]model.Server) string {
	cfg := parseStepConfig(step.ConfigJSON)
	typeName := strings.ToLower(stringValue(cfg, "type", string(model.TunnelTypeSSH)))
	switch model.TunnelType(typeName) {
	case model.TunnelTypeSSH:
		sshPort := intValueFromMap(cfg, "ssh_port", 0)
		if sshPort == 0 {
			sshPort = servers[targetServerID].SSHPort
		}
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
	// SSH local forwarding listens only on loopback. Keeping it in a separate
	// internal pool prevents a one-port server range from colliding with the
	// public inbound that already owns that port.
	if salt == 31 {
		return proxyPathAvailablePort(server, pathID*193, position*71+salt, 20000, 29999, inbounds)
	}
	start, end := proxyPathServerPortRange(server)
	protocol := model.ForwardProtocolTCP
	if salt == 47 {
		protocol = model.ForwardProtocolUDP
	}
	return proxyPathAvailablePortForProtocol(server, pathID*193, position*71+salt, start, end, protocol, false, inbounds)
}

func proxyPathAvailablePort(server model.Server, pathSeed int64, positionSeed, start, end int, inbounds map[int64]model.Inbound) int {
	return proxyPathAvailablePortForProtocol(server, pathSeed, positionSeed, start, end, "", true, inbounds)
}

func proxyPathAvailablePortForProtocol(server model.Server, pathSeed int64, positionSeed, start, end int, protocol model.ForwardProtocol, fallback bool, inbounds map[int64]model.Inbound) int {
	occupied := map[int]bool{}
	for _, inbound := range inbounds {
		// Disabled inbounds are reserved too. Allocation is deterministic, so
		// handing a disabled inbound's port to a generated listener would create a
		// listener conflict the operator cannot resolve by re-enabling it.
		if inbound.ServerID == server.ID && inbound.Port > 0 && (protocol == "" || proxyPathInboundUsesProtocol(inbound, protocol)) {
			occupied[inbound.Port] = true
		}
	}
	if server.SSHPort > 0 && protocol != model.ForwardProtocolUDP {
		occupied[server.SSHPort] = true
	}
	choose := func(rangeStart, rangeEnd int) int {
		span := rangeEnd - rangeStart + 1
		if span <= 0 {
			return 0
		}
		seed := int((pathSeed + int64(positionSeed)) % int64(span))
		if seed < 0 {
			seed = -seed
		}
		for offset := 0; offset < span; offset++ {
			candidate := rangeStart + (seed+offset)%span
			if !occupied[candidate] {
				return candidate
			}
		}
		return 0
	}
	if port := choose(start, end); port != 0 {
		return port
	}
	if !fallback {
		return 0
	}
	// Hidden proxy-path listeners are implementation details rather than user
	// entry ports. When the configured range is exhausted, use the dedicated
	// internal pool instead of reusing an occupied public port.
	return choose(30000, 60000)
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
	return 30000, 60000
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
