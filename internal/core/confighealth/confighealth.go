// Package confighealth derives the operator-facing configuration health report
// from an already-loaded topology snapshot.
//
// Every existing OBoard validation runs at write time or at configuration
// generation time. That leaves two gaps this package closes. A row that became
// invalid after it was stored - because another resource it referenced was
// removed, because a server port range was narrowed, or because it predates a
// tightened rule - is silently wrong until a deployment fails. And such a row
// cannot be repaired through the normal form at all, because the store
// re-validates the whole record on write and rejects it before the operator's
// correction lands.
//
// The evaluator is deliberately pure: it takes a snapshot, returns findings,
// and performs no I/O. That is what lets the Controller derive it once per
// routing revision inside the existing immutable snapshot instead of scanning
// the database per request.
package confighealth

import (
	"sort"

	"github.com/OboardProject/oboard/internal/model"
)

// Severity ranks a finding by what it costs the operator.
const (
	// SeverityBlocking marks configuration that cannot deploy: generation for
	// the owning server fails, or the next save of the resource is rejected.
	SeverityBlocking = "blocking"
	// SeverityWarning marks configuration that deploys but does not behave the
	// way the topology reads.
	SeverityWarning = "warning"
	// SeverityNotice marks normalization debt: the runtime is correct, but the
	// stored document carries content the current model does not use.
	SeverityNotice = "notice"
)

// Scope names the resource family a finding belongs to. The Web console groups
// the cleanup dialog by these.
const (
	ScopeInbound     = "inbound"
	ScopeProxyPath   = "proxy_path"
	ScopeRoutingRule = "routing_rule"
	ScopeDNSPolicy   = "dns_policy"
	// ScopeSyncLane is a per-server delivery lane rather than a stored record.
	// Its findings are about the binding between a version and the content that
	// version was issued for, which is the one kind of breakage an operator can
	// neither see in a form nor fix by editing one.
	ScopeSyncLane = "sync_lane"
)

// Remedy kinds, in increasing force. Every one of them is an operation the
// normal form cannot perform on an already-invalid row.
const (
	// RemedyNone marks a finding the operator has to resolve by hand, because
	// no automatic action can preserve intent.
	RemedyNone = "none"
	// RemedyNormalize rewrites the stored protocol document with the
	// unsupported content removed, bypassing the write-time validation that
	// would otherwise reject the corrected row.
	RemedyNormalize = "normalize"
	// RemedyDisable flips the resource off so the fleet can deploy again while
	// the operator decides what to do with it. Reversible.
	RemedyDisable = "disable"
	// RemedyDelete removes an orphan row that references something that no
	// longer exists.
	RemedyDelete = "delete"
	// RemedyResync drops a delivery lane's version binding so the next delivery
	// is issued a new version. It changes no configuration: the content stays
	// exactly what it was, only the number it is delivered under moves, which is
	// what lets a node that refuses the current version accept it.
	RemedyResync = "resync"
)

// Remedy describes the cleanup this finding supports.
type Remedy struct {
	Kind string `json:"kind"`
	// Summary is the operator-facing description of what the action does.
	Summary string `json:"summary,omitempty"`
	// Fields are the document paths a normalize action removes. It is a
	// preview only: the apply path recomputes them from the live row.
	Fields []string `json:"fields,omitempty"`
	// Destructive marks an action that loses configuration. The console
	// requires a second confirmation for these.
	Destructive bool `json:"destructive,omitempty"`
}

// Finding is one detected configuration problem.
type Finding struct {
	// Code is the stable identifier the cleanup API resolves an action against.
	// The client selects a finding by code plus resource; it never describes
	// the mutation itself.
	Code         string `json:"code"`
	Severity     string `json:"severity"`
	Scope        string `json:"scope"`
	ResourceID   int64  `json:"resource_id"`
	ResourceName string `json:"resource_name,omitempty"`
	ServerID     int64  `json:"server_id,omitempty"`
	ServerName   string `json:"server_name,omitempty"`
	Title        string `json:"title"`
	Detail       string `json:"detail,omitempty"`
	// Path locates the offending field inside the stored document when the
	// problem is field-level.
	Path   string `json:"path,omitempty"`
	Remedy Remedy `json:"remedy"`
}

// Summary is the counts-only projection. The dashboard carries this and never
// the findings array, so a poll against an unchanged revision stays flat.
type Summary struct {
	Blocking  int  `json:"blocking"`
	Warning   int  `json:"warning"`
	Notice    int  `json:"notice"`
	Total     int  `json:"total"`
	Truncated bool `json:"truncated,omitempty"`
}

// Clean reports whether there is nothing for the operator to act on. The
// console renders no card at all in that case.
func (s Summary) Clean() bool { return s.Total == 0 }

// Report is the evaluation result for one topology snapshot.
type Report struct {
	Summary  Summary   `json:"summary"`
	Findings []Finding `json:"findings"`
	// ServerIDs maps a server to its blocking finding count so the deployment
	// confirmation can warn about exactly the targets it is about to push.
	BlockingByServer map[int64]int `json:"blocking_by_server,omitempty"`
}

// findingLimit bounds the report. A database that is broken at scale must not
// be able to produce a multi-megabyte response or a long evaluation.
const findingLimit = 500

// Input is the topology the evaluator reads. Reference tables that are only
// checked for existence are passed as ID sets so the snapshot does not have to
// carry whole rows it will never display.
type Input struct {
	Servers           []model.Server
	Inbounds          []model.Inbound
	ProxyPaths        []model.ProxyPath
	ProxyPathSteps    []model.ProxyPathStep
	RoutingRules      []model.RoutingRule
	RoutingRuleSets   []model.RoutingRuleSet
	Outbounds         []model.Outbound
	ExternalOutbounds []model.ExternalOutbound
	DNSLists          []model.DNSList
	ServerDNSPolicies []model.ServerDNSPolicy

	// FamilySplitTemplateIDs, UsableNodePresetIDs and UsableSnellProfileIDs
	// contain only the rows a reference may legally resolve to. A disabled
	// preset is excluded on purpose: resolving one fails exactly like a
	// missing one, so the operator must see it the same way.
	FamilySplitTemplateIDs map[int64]bool
	UsableNodePresetIDs    map[int64]bool
	UsableSnellProfileIDs  map[int64]bool
	CertificateIDs         map[int64]bool
	DNSCredentialIDs       map[int64]bool

	// UsersLanes and ProbeLanes are delivery state, not stored configuration.
	// They are passed in as plain facts - what the Controller bound, what the
	// node reports - so the evaluator stays pure and the caller keeps ownership
	// of how those facts are read.
	UsersLanes []UsersLane
	ProbeLanes []ProbeLane
}

// Evaluate derives the report. It never returns an error: an input it cannot
// interpret becomes a finding, because refusing to report would leave the
// operator with exactly the blind spot this package exists to remove.
func Evaluate(in Input) Report {
	c := &collector{input: in}
	c.index()
	c.checkInbounds()
	c.checkProxyPaths()
	c.checkRoutingRules()
	c.checkDNSPolicies()
	c.checkSyncLanes()
	return c.report()
}

type collector struct {
	input Input

	serverByID  map[int64]model.Server
	inboundByID map[int64]model.Inbound
	pathByID    map[int64]model.ProxyPath
	stepByID    map[int64]model.ProxyPathStep
	stepsByPath map[int64][]model.ProxyPathStep
	ruleSetIDs  map[int64]bool
	outboundIDs map[int64]bool
	externalIDs map[int64]bool
	dnsListByID map[int64]model.DNSList

	findings  []Finding
	truncated bool
}

func (c *collector) index() {
	c.serverByID = make(map[int64]model.Server, len(c.input.Servers))
	for _, item := range c.input.Servers {
		c.serverByID[item.ID] = item
	}
	c.inboundByID = make(map[int64]model.Inbound, len(c.input.Inbounds))
	for _, item := range c.input.Inbounds {
		c.inboundByID[item.ID] = item
	}
	c.pathByID = make(map[int64]model.ProxyPath, len(c.input.ProxyPaths))
	for _, item := range c.input.ProxyPaths {
		c.pathByID[item.ID] = item
	}
	c.stepByID = make(map[int64]model.ProxyPathStep, len(c.input.ProxyPathSteps))
	c.stepsByPath = make(map[int64][]model.ProxyPathStep)
	for _, item := range c.input.ProxyPathSteps {
		c.stepByID[item.ID] = item
		c.stepsByPath[item.PathID] = append(c.stepsByPath[item.PathID], item)
	}
	c.ruleSetIDs = make(map[int64]bool, len(c.input.RoutingRuleSets))
	for _, item := range c.input.RoutingRuleSets {
		c.ruleSetIDs[item.ID] = true
	}
	c.outboundIDs = make(map[int64]bool, len(c.input.Outbounds))
	for _, item := range c.input.Outbounds {
		c.outboundIDs[item.ID] = true
	}
	c.externalIDs = make(map[int64]bool, len(c.input.ExternalOutbounds))
	for _, item := range c.input.ExternalOutbounds {
		c.externalIDs[item.ID] = true
	}
	c.dnsListByID = make(map[int64]model.DNSList, len(c.input.DNSLists))
	for _, item := range c.input.DNSLists {
		c.dnsListByID[item.ID] = item
	}
}

func (c *collector) add(f Finding) {
	if len(c.findings) >= findingLimit {
		c.truncated = true
		return
	}
	if server, ok := c.serverByID[f.ServerID]; ok {
		f.ServerName = server.Name
		// A delivery lane's resource is the server itself, so the console would
		// otherwise render an unnamed row for the one scope whose resource has
		// an obvious name.
		if f.Scope == ScopeSyncLane && f.ResourceName == "" {
			f.ResourceName = server.Name
		}
	}
	c.findings = append(c.findings, f)
}

// severityRank orders findings so the console shows what blocks a deployment
// before what is merely untidy.
func severityRank(severity string) int {
	switch severity {
	case SeverityBlocking:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

func (c *collector) report() Report {
	sort.SliceStable(c.findings, func(i, j int) bool {
		a, b := c.findings[i], c.findings[j]
		if rank := severityRank(a.Severity) - severityRank(b.Severity); rank != 0 {
			return rank < 0
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.ResourceID != b.ResourceID {
			return a.ResourceID < b.ResourceID
		}
		return a.Code < b.Code
	})
	out := Report{Findings: c.findings, BlockingByServer: map[int64]int{}}
	if out.Findings == nil {
		out.Findings = []Finding{}
	}
	for _, f := range c.findings {
		switch f.Severity {
		case SeverityBlocking:
			out.Summary.Blocking++
			if f.ServerID > 0 {
				out.BlockingByServer[f.ServerID]++
			}
		case SeverityWarning:
			out.Summary.Warning++
		default:
			out.Summary.Notice++
		}
	}
	out.Summary.Total = len(c.findings)
	out.Summary.Truncated = c.truncated
	return out
}
