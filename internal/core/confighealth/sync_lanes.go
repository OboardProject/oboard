package confighealth

import "fmt"

// Delivery lanes are version gates. The Controller binds a version to a content
// identity, the node refuses a version it already holds with different content,
// and the Controller re-derives the same binding for as long as the content does
// not change. When the two sides end up disagreeing about what one version
// means, neither can leave that state on its own: the version only moves when
// the content moves, and the content is already what the node should run.
//
// Every other check in this package reads stored configuration. These two read
// what a node reports about itself, because that is the only place the
// disagreement exists - the Controller's own ledger says the node is confirmed,
// and the node's refusal is a local log line.
//
// The remedy is the same for both lanes and is not a configuration edit: drop
// the version binding so the next delivery is issued a new version, which is the
// one thing a node's gate always accepts.
const (
	// CodeUsersRevisionConflict marks a node running the credential set of one
	// users revision while the Controller has bound that revision to a different
	// one. Blocking: until it clears, the node receives no credential change and
	// no authorization renewal, so its users stop being served a few minutes
	// after the last lease it did receive expires.
	CodeUsersRevisionConflict = "sync.users.revision_conflict"
	// CodeProbeVersionConflict marks a node running a different probe plan than
	// the one bound to the version it holds. Warning: latency results keep
	// arriving for the plan the node kept, but every plan change is silently
	// refused from then on.
	CodeProbeVersionConflict = "sync.probe.version_conflict"
)

// UsersLane is one server's runtime users delivery state, as the Controller
// recorded it and as the node reports it.
type UsersLane struct {
	ServerID int64
	// DesiredRevision and DesiredDigest are the binding the Controller holds.
	// The digest is the content identity the revision was allocated against.
	DesiredRevision int64
	DesiredDigest   string
	// AppliedRevision and AppliedContentDigest are what the node reports it
	// runs. An empty content digest means the node predates reporting one and
	// nothing can be concluded from it.
	AppliedRevision      int64
	AppliedContentDigest string
	// PendingReason is the lane's recorded reason, used so an acknowledged
	// refusal is reported even before the node's own report confirms it.
	PendingReason string
	// Conflicted marks a PendingReason the store defines as a refusal. The
	// evaluator does not import the store, so the caller resolves it.
	Conflicted bool
}

// ProbeLane is one server's latency probe plan binding.
type ProbeLane struct {
	ServerID int64
	// PlanVersion and PlanDigest are the binding the Controller holds.
	PlanVersion int64
	PlanDigest  string
	// AppliedVersion and AppliedDigest are the plan the node reports it runs.
	// A zero version means the node has not reported one.
	AppliedVersion int64
	AppliedDigest  string
}

func (c *collector) checkSyncLanes() {
	for _, lane := range c.input.UsersLanes {
		c.checkUsersLane(lane)
	}
	for _, lane := range c.input.ProbeLanes {
		c.checkProbeLane(lane)
	}
}

func (c *collector) checkUsersLane(lane UsersLane) {
	if lane.ServerID <= 0 || lane.DesiredRevision <= 0 {
		return
	}
	detail := ""
	switch {
	case lane.Conflicted:
		detail = fmt.Sprintf("节点拒绝了第 %d 版用户配置，提示该版本已按其它内容安装。", lane.DesiredRevision)
	case lane.AppliedRevision == lane.DesiredRevision &&
		lane.AppliedContentDigest != "" && lane.DesiredDigest != "" &&
		lane.AppliedContentDigest != lane.DesiredDigest:
		detail = fmt.Sprintf("节点上运行的第 %d 版用户配置与面板记录的同版本内容不一致。", lane.DesiredRevision)
	default:
		return
	}
	c.add(Finding{
		Code:       CodeUsersRevisionConflict,
		Severity:   SeverityBlocking,
		Scope:      ScopeSyncLane,
		ResourceID: lane.ServerID,
		ServerID:   lane.ServerID,
		Title:      "用户配置版本冲突，节点已停止接收更新",
		Detail:     detail + "版本号只在内容变化时前进，因此重复下发不会生效；需要重新分配一个版本号。",
		Remedy: Remedy{
			Kind:    RemedyResync,
			Summary: "重新分配版本号并重新下发用户配置",
		},
	})
}

func (c *collector) checkProbeLane(lane ProbeLane) {
	if lane.ServerID <= 0 || lane.PlanVersion <= 0 || lane.AppliedVersion <= 0 {
		return
	}
	detail := ""
	switch {
	case lane.AppliedVersion > lane.PlanVersion:
		detail = fmt.Sprintf("节点持有的测速计划版本（%d）比面板记录的（%d）更新，面板下发的计划会被一直拒绝。", lane.AppliedVersion, lane.PlanVersion)
	case lane.AppliedVersion == lane.PlanVersion &&
		lane.AppliedDigest != "" && lane.PlanDigest != "" &&
		lane.AppliedDigest != lane.PlanDigest:
		detail = fmt.Sprintf("节点上运行的测速计划与面板记录的第 %d 版内容不一致。", lane.PlanVersion)
	default:
		return
	}
	c.add(Finding{
		Code:       CodeProbeVersionConflict,
		Severity:   SeverityWarning,
		Scope:      ScopeSyncLane,
		ResourceID: lane.ServerID,
		ServerID:   lane.ServerID,
		Title:      "测速计划版本冲突，节点不再接受新计划",
		Detail:     detail + "重新分配版本号后，节点会接受当前计划。",
		Remedy: Remedy{
			Kind:    RemedyResync,
			Summary: "重新分配版本号并重新下发测速计划",
		},
	})
}
