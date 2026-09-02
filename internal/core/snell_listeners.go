package core

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// errNoSnellInboundSecret marks an inbound whose stable PSK never got
// persisted. Controller generates one at create time, so reaching this means
// the stored desired state is incomplete rather than merely unusual.
var errNoSnellInboundSecret = errors.New("snell psk required (config_json.psk)")

// Snell is a single-user protocol on the wire. Both sing-snell v5 and v6 derive
// the AEAD key from the server PSK alone, and the per-user `userkey` sing-box
// accepts in multi-user mode is only an identity tag read after decryption — it
// grants no cryptographic isolation between users. Surge, Mihomo, Egern,
// Shadowrocket and Surfboard have no userkey field at all, so a multi-user
// listener rejects every one of them: they connect with the PSK, send an empty
// client id, and sing-snell answers ErrBadUserKey.
//
// OBoard therefore fans one panel Snell inbound out into one single-user
// sing-box listener per authorized identity, each with its own port from the
// server auto range and its own PSK derived from the inbound secret. That is
// compatible with every client and upgrades isolation from "none" to real
// per-user keys: revoking a user revokes a key nobody else holds.
type snellUserListener struct {
	InboundID int64
	ServerID  int64
	User      model.User
	PathID    int64
	Port      int
	Tag       string
	PSK       string
}

// snellUserSyntheticIDBase keeps the reservation-only inbound IDs of generated
// Snell listeners in a bit field no other synthetic listener uses. Proxy paths
// already claim 1<<43 (shared transparent), 1<<45 (per-hop internal) and
// 1<<45|1<<44 (outer reservation).
const snellUserSyntheticIDBase = int64(1) << 46

// snellUserInboundTag names one generated listener. User IDs are signed: proxy
// path link users carry the negated path id and the placeholder identity uses
// zero, so the sign is encoded rather than dropped to keep tags unique.
func snellUserInboundTag(inboundID, userID, pathID int64) string {
	name := fmt.Sprintf("in-%d-u%d", inboundID, userID)
	if userID < 0 {
		name = fmt.Sprintf("in-%d-l%d", inboundID, -userID)
	}
	if pathID > 0 {
		name += fmt.Sprintf("-p%d", pathID)
	}
	return name
}

// snellUserPSK derives the per-user server PSK. The root is the inbound's
// config_json.psk, which Controller already generates and persists as a stable
// credential that user changes never rotate, so no new table or migration is
// needed. user.ProxyPassword joins the seed so rotating a user's password
// rotates their Snell credential the same way it does for every other
// protocol; device-bound users arrive here with ProxyPassword already rewritten
// per inbound and path by credentialUser, so device binding flows through too.
//
// deterministicSecret returns 43 base64url characters, satisfying the 12-255
// byte PSK contract sing-snell v6 enforces.
func snellUserPSK(inboundSecret string, inbound model.Inbound, user model.User, pathID int64) string {
	return deterministicSecret(fmt.Sprintf("%s:snell:inbound:%d:user:%d:path:%d:%s",
		inboundSecret, inbound.ID, user.ID, pathID, user.ProxyPassword))
}

// planSnellUserListeners projects the generated listeners of every Snell
// inbound on every known server, not just the server whose config is being
// generated. Port ownership must not depend on deployment scope: a focused
// deploy that skipped a server would leave its owners unclaimed and
// StaleProxyPathPortAllocationIDs would release ports that are still serving
// live clients.
//
// The returned synthetic inbounds exist only to reserve the chosen ports for
// the rest of this projection. proxyPathAvailablePort only sees conflicts
// through the inbound map it is given, so without them two Snell users — or a
// Snell user and a proxy path hop — could be handed the same port.
func planSnellUserListeners(inbounds []model.Inbound, servers []model.Server, users []model.User, opts ConfigOptions) (map[int64][]snellUserListener, []model.Inbound, error) {
	snellInbounds := make([]model.Inbound, 0)
	for _, inbound := range inbounds {
		if inbound.Protocol == model.ProtocolSnell && inbound.Enabled {
			snellInbounds = append(snellInbounds, inbound)
		}
	}
	if len(snellInbounds) == 0 {
		return nil, nil, nil
	}
	sort.SliceStable(snellInbounds, func(i, j int) bool { return snellInbounds[i].ID < snellInbounds[j].ID })

	serverByID := map[int64]model.Server{}
	for _, item := range servers {
		serverByID[item.ID] = item
	}
	inboundByID := map[int64]model.Inbound{}
	for _, item := range inbounds {
		inboundByID[item.ID] = item
	}

	plan := map[int64][]snellUserListener{}
	reservations := []model.Inbound{}
	for _, inbound := range snellInbounds {
		host, ok := serverByID[inbound.ServerID]
		if !ok {
			// The inbound's server is outside this projection's data set, so
			// there is nothing to allocate against. Config generation for that
			// server will fail on its own terms.
			continue
		}
		if inboundUsesTransparentProcessing(inbound.ID, opts.ProxyPaths, opts.ProxyPathSteps) {
			continue
		}
		secret, err := snellInboundSecret(inbound)
		if err != nil {
			return nil, nil, fmt.Errorf("snell inbound %s: %w", inbound.Name, err)
		}
		accountedUsers, listenerUsers, err := resolveInboundUsers(inbound, users, opts, host.ChainSecret)
		if err != nil {
			return nil, nil, err
		}
		if inbound.AdvertisePort > 0 && len(accountedUsers) > 1 {
			return nil, nil, markInvalidDesiredState(fmt.Errorf(
				"snell 入站 %s 配置了对外端口 %d，但当前有 %d 个可订阅的逐用户或逐分支实例；一个对外端口只能映射一个客户端运行端口",
				inbound.Name, inbound.AdvertisePort, len(accountedUsers)))
		}
		listenIP := EffectiveListenIP(host, inbound.ListenIP)
		start, end := proxyPathServerPortRange(host)
		for _, user := range listenerUsers {
			pathID := runtimePathIDFromUsername(user.Username)
			listener := snellUserListener{
				InboundID: inbound.ID,
				ServerID:  host.ID,
				User:      user,
				PathID:    pathID,
				Tag:       snellUserInboundTag(inbound.ID, user.ID, pathID),
				PSK:       snellUserPSK(secret, inbound, user, pathID),
			}
			seed := inbound.ID*1000003 + user.ID*10007 + pathID*101
			listener.Port = opts.PortLedger.resolve(PortRequirement{
				Kind:           model.ProxyPathPortKindSnellUser,
				ScopeKey:       snellUserPortScopeKey(inbound.ID, user.ID, pathID),
				ServerID:       host.ID,
				Pool:           model.PortPoolPublic,
				ListenIP:       listenIP,
				Network:        model.ForwardProtocolTCP,
				PolicyRevision: serverPortPolicyRevision(host),
				Allocate: func() int {
					return proxyPathAvailablePort(host, seed, 0, start, end, listenIP, inboundByID)
				},
			})
			if listener.Port <= 0 {
				return nil, nil, markInvalidDesiredState(fmt.Errorf(
					"snell 入站 %s 需要为每个用户分配一个独立端口，服务器 %s 的自动端口段 %d-%d 已耗尽（本入站需要 %d 个端口）",
					inbound.Name, firstNonEmpty(host.Name, fmt.Sprintf("%d", host.ID)), start, end, len(listenerUsers)))
			}
			reservation := model.Inbound{
				ID:       -(snellUserSyntheticIDBase + int64(len(reservations))),
				ServerID: host.ID,
				Name:     listener.Tag,
				Protocol: model.ProtocolSnell,
				ListenIP: listenIP,
				Port:     listener.Port,
				Enabled:  true,
			}
			inboundByID[reservation.ID] = reservation
			reservations = append(reservations, reservation)
			plan[inbound.ID] = append(plan[inbound.ID], listener)
		}
	}
	return plan, reservations, nil
}

// snellInboundSecret returns the inbound-level PSK that seeds every per-user
// PSK. Controller persists it at create time (resolveSnellProfileIntoInbound),
// so an inbound without one is a desired-state defect rather than a runtime
// condition to paper over.
func snellInboundSecret(inbound model.Inbound) (string, error) {
	extra := parseExtra(inbound.ConfigJSON)
	version, err := snellPanelVersion(extra)
	if err != nil {
		return "", err
	}
	secret := stringValue(extra, "psk", "")
	if secret == "" {
		return "", errNoSnellInboundSecret
	}
	if err := validateSnellPSKLength(secret, version); err != nil {
		return "", err
	}
	return secret, nil
}

// snellListenerInbound renders one generated listener. It deliberately never
// emits `users`: an empty user list is what keeps sing-box on the single-user
// snellv5.NewService / snellv6.NewService path, which authenticates with the
// PSK alone and therefore accepts every client.
func snellListenerInbound(inbound model.Inbound, listener snellUserListener) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	panelVersion, err := snellPanelVersion(extra)
	if err != nil {
		return nil, err
	}
	serverVersion, err := SnellServerVersion(panelVersion)
	if err != nil {
		return nil, err
	}
	item := map[string]any{
		"type":        "snell",
		"tag":         listener.Tag,
		"listen":      inbound.ListenIP,
		"listen_port": listener.Port,
		"version":     serverVersion,
		"psk":         listener.PSK,
	}
	if panelVersion == SnellVersionV4 {
		obfs, err := normalizeSnellObfsMode(stringValue(extra, "obfs_mode", "none"))
		if err != nil {
			return nil, err
		}
		if obfs != "none" {
			item["obfs_mode"] = obfs
		}
	} else {
		mode, err := normalizeSnellV6Mode(stringValue(extra, "mode", "default"))
		if err != nil {
			return nil, err
		}
		if mode != "default" {
			item["mode"] = mode
		}
	}
	applyAllowed(item, extra, "tcp_fast_open")
	return item, nil
}

// snellUserPortScopeKey is the ledger owner key of one generated listener. The
// projection and every read-only consumer must derive it identically, or a
// subscription would advertise a port the kernel does not listen on.
func snellUserPortScopeKey(inboundID, userID, pathID int64) string {
	return fmt.Sprintf("inbound:%d:user:%d:path:%d", inboundID, userID, pathID)
}

// SnellSubscriptionNode renders the client-facing node for one identity on one
// branch. The generated listener still runs on its ledger port, but an explicit
// advertise_port is the public NAT/forwarding entry and therefore replaces only
// the client-facing server_port. It reads the ledger without allocating, so a
// user whose listener has not been deployed yet yields ok=false and is omitted
// rather than handed a port that does not exist.
//
// The caller must pass the same identity the config projection used — the
// branch user for a proxy path branch, and in both cases the credential-scoped
// user from UserCredentialForRoute — otherwise the derived PSK will not match.
func SnellSubscriptionNode(ledger *ProxyPathPortLedger, user model.User, inbound model.Inbound, server model.Server, pathID int64) (map[string]any, bool, error) {
	if inbound.AdvertisePort > 0 && activeSnellClientListenerCount(ledger, inbound) > 1 {
		return nil, false, markInvalidDesiredState(fmt.Errorf(
			"snell 入站 %s 的对外端口 %d 对应多个客户端运行端口，请先收敛为单个用户或分支并重新部署",
			inbound.Name, inbound.AdvertisePort))
	}
	return snellUserNode(ledger, user, inbound, server, pathID)
}

func activeSnellClientListenerCount(ledger *ProxyPathPortLedger, inbound model.Inbound) int {
	if ledger == nil {
		return 0
	}
	prefix := fmt.Sprintf("inbound:%d:user:", inbound.ID)
	count := 0
	for key, owner := range ledger.owners {
		if key.Kind != model.ProxyPathPortKindSnellUser || key.ServerID != inbound.ServerID || !strings.HasPrefix(key.ScopeKey, prefix) || owner.activeGeneration() == nil {
			continue
		}
		userPart := strings.TrimPrefix(key.ScopeKey, prefix)
		userPart, _, _ = strings.Cut(userPart, ":path:")
		userID, err := strconv.ParseInt(userPart, 10, 64)
		if err == nil && userID > 0 {
			count++
		}
	}
	return count
}

func snellUserNode(ledger *ProxyPathPortLedger, user model.User, inbound model.Inbound, server model.Server, pathID int64) (map[string]any, bool, error) {
	extra := parseExtra(inbound.ConfigJSON)
	panelVersion, err := snellPanelVersion(extra)
	if err != nil {
		return nil, false, err
	}
	clientVersion, err := SnellClientVersion(panelVersion)
	if err != nil {
		return nil, false, err
	}
	secret, err := snellInboundSecret(inbound)
	if err != nil {
		return nil, false, err
	}
	runtimePort, ok := ledger.LookupActive(model.ProxyPathPortKindSnellUser, snellUserPortScopeKey(inbound.ID, user.ID, pathID), inbound.ServerID)
	if !ok {
		return nil, false, nil
	}
	serverPort := runtimePort
	if inbound.AdvertisePort > 0 {
		serverPort = inbound.AdvertisePort
	}
	node := map[string]any{
		"type":        "snell",
		"tag":         inbound.Name,
		"server":      server.EntryAddress,
		"server_port": serverPort,
		"version":     clientVersion,
		"psk":         snellUserPSK(secret, inbound, user, pathID),
	}
	if clientVersion == SnellVersionV4 {
		obfs, err := normalizeSnellObfsMode(stringValue(extra, "obfs_mode", "none"))
		if err != nil {
			return nil, false, err
		}
		if obfs != "none" {
			node["obfs_mode"] = obfs
			if host := stringValue(extra, "obfs_host", ""); host != "" {
				node["obfs_host"] = host
			}
		}
	} else {
		mode, err := normalizeSnellV6Mode(stringValue(extra, "mode", "default"))
		if err != nil {
			return nil, false, err
		}
		if mode != "default" {
			node["mode"] = mode
		}
	}
	if snellReuse(extra) {
		node["reuse"] = true
	}
	applyAllowed(node, extra, "tcp_fast_open")
	return node, true, nil
}
