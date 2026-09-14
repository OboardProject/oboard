package confighealth

import (
	"fmt"
	"sort"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func (c *collector) checkProxyPaths() {
	for _, path := range c.input.ProxyPaths {
		c.checkProxyPathRoot(path)
		c.checkProxyPathShape(path)
	}
	c.checkProxyPathSteps()
	c.checkDuplicateDirectPaths()
}

// checkProxyPathRoot validates the inbound a path starts from. A family branch
// carries a nullable inbound on purpose - it inherits the grafting rule's
// source - so only a非空 reference is checked there.
func (c *collector) checkProxyPathRoot(path model.ProxyPath) {
	if path.InboundID <= 0 {
		if path.Kind != model.ProxyPathKindFamilyBranch {
			c.add(Finding{
				Code:         "proxy_path.inbound.unset",
				Severity:     SeverityBlocking,
				Scope:        ScopeProxyPath,
				ResourceID:   path.ID,
				ResourceName: path.Name,
				Title:        "链路没有绑定入口",
				Detail:       "链路必须从一个入口开始，当前记录缺少入口绑定",
				Remedy:       Remedy{Kind: RemedyDelete, Summary: "删除这条无法成形的链路", Destructive: true},
			})
		}
		return
	}
	inbound, ok := c.inboundByID[path.InboundID]
	if !ok {
		c.add(Finding{
			Code:         "proxy_path.inbound.missing",
			Severity:     SeverityBlocking,
			Scope:        ScopeProxyPath,
			ResourceID:   path.ID,
			ResourceName: path.Name,
			Title:        "链路绑定的入口已不存在",
			Detail:       fmt.Sprintf("入口 %d 已被删除", path.InboundID),
			Remedy:       Remedy{Kind: RemedyDelete, Summary: "删除这条指向已删除入口的链路", Destructive: true},
		})
		return
	}
	if path.Enabled && !inbound.Enabled {
		c.add(Finding{
			Code:         "proxy_path.inbound.disabled",
			Severity:     SeverityWarning,
			Scope:        ScopeProxyPath,
			ResourceID:   path.ID,
			ResourceName: path.Name,
			ServerID:     inbound.ServerID,
			Title:        "链路已启用但入口处于停用状态",
			Detail:       fmt.Sprintf("入口「%s」(#%d) 已停用，这条链路不会有任何流量", inbound.Name, inbound.ID),
			Remedy:       Remedy{Kind: RemedyDisable, Summary: "同步停用这条链路，避免拓扑显示为可用"},
		})
	}
}

// checkProxyPathShape reports a path whose own structure is incomplete.
func (c *collector) checkProxyPathShape(path model.ProxyPath) {
	if path.Kind == model.ProxyPathKindFamilyBranch {
		if path.TemplateID != nil && *path.TemplateID > 0 && !c.input.FamilySplitTemplateIDs[*path.TemplateID] {
			c.add(Finding{
				Code:         "proxy_path.family_template.missing",
				Severity:     SeverityBlocking,
				Scope:        ScopeProxyPath,
				ResourceID:   path.ID,
				ResourceName: path.Name,
				Title:        "分流分支所属的双栈模板已不存在",
				Detail:       fmt.Sprintf("模板 %d 已被删除，这个分支不会被任何规则引用", *path.TemplateID),
				Remedy:       Remedy{Kind: RemedyDelete, Summary: "删除这个没有模板归属的分支", Destructive: true},
			})
		}
		if path.BranchSourceStepID != nil && *path.BranchSourceStepID > 0 {
			if _, ok := c.stepByID[*path.BranchSourceStepID]; !ok {
				c.add(Finding{
					Code:         "proxy_path.branch_source.missing",
					Severity:     SeverityBlocking,
					Scope:        ScopeProxyPath,
					ResourceID:   path.ID,
					ResourceName: path.Name,
					Title:        "分流分支的接入节点已不存在",
					Detail:       fmt.Sprintf("链路节点 %d 已被删除，这个分支没有接入位置", *path.BranchSourceStepID),
					Remedy:       Remedy{Kind: RemedyDelete, Summary: "删除这个无法接入的分支", Destructive: true},
				})
			}
		}
	}
	if !path.Enabled {
		return
	}
	if path.Kind == model.ProxyPathKindDirect {
		return
	}
	if len(c.stepsByPath[path.ID]) == 0 {
		c.add(Finding{
			Code:         "proxy_path.steps.empty",
			Severity:     SeverityWarning,
			Scope:        ScopeProxyPath,
			ResourceID:   path.ID,
			ResourceName: path.Name,
			Title:        "链路已启用但没有任何中转节点",
			Detail:       "这条链路等同于直连，拓扑显示与实际行为不一致",
			Remedy:       Remedy{Kind: RemedyDisable, Summary: "停用这条空链路"},
		})
	}
}

// checkProxyPathSteps reports steps whose target resource no longer exists.
// The finding is attached to the owning path so cleanup acts on something the
// operator can recognise in the topology view.
func (c *collector) checkProxyPathSteps() {
	steps := make([]model.ProxyPathStep, len(c.input.ProxyPathSteps))
	copy(steps, c.input.ProxyPathSteps)
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].PathID != steps[j].PathID {
			return steps[i].PathID < steps[j].PathID
		}
		return steps[i].Position < steps[j].Position
	})
	for _, step := range steps {
		path, ok := c.pathByID[step.PathID]
		if !ok {
			c.add(Finding{
				Code:       "proxy_path.step.orphan",
				Severity:   SeverityWarning,
				Scope:      ScopeProxyPath,
				ResourceID: step.ID,
				Title:      "链路节点没有归属链路",
				Detail:     fmt.Sprintf("节点 %d 属于已删除的链路 %d", step.ID, step.PathID),
				Remedy:     Remedy{Kind: RemedyDelete, Summary: "删除这个残留的链路节点", Destructive: true},
			})
			continue
		}
		c.checkProxyPathStepTargets(path, step)
	}
}

func (c *collector) checkProxyPathStepTargets(path model.ProxyPath, step model.ProxyPathStep) {
	report := func(code, title, detail string, severity string) {
		c.add(Finding{
			Code:         code,
			Severity:     severity,
			Scope:        ScopeProxyPath,
			ResourceID:   path.ID,
			ResourceName: path.Name,
			Title:        title,
			Detail:       detail,
			Path:         fmt.Sprintf("steps[%d]", step.Position),
			Remedy:       Remedy{Kind: RemedyDisable, Summary: "先停用这条链路，再修复或移除该节点"},
		})
	}
	if step.ServerID != nil && *step.ServerID > 0 {
		if _, ok := c.serverByID[*step.ServerID]; !ok {
			report("proxy_path.step.server_missing", "链路节点指向的服务器已不存在",
				fmt.Sprintf("第 %d 跳引用的服务器 %d 已被删除", step.Position, *step.ServerID), SeverityBlocking)
		}
	}
	if step.InboundID != nil && *step.InboundID > 0 {
		inbound, ok := c.inboundByID[*step.InboundID]
		switch {
		case !ok:
			report("proxy_path.step.inbound_missing", "链路节点指向的入口已不存在",
				fmt.Sprintf("第 %d 跳引用的入口 %d 已被删除", step.Position, *step.InboundID), SeverityBlocking)
		case path.Enabled && !inbound.Enabled:
			report("proxy_path.step.inbound_disabled", "链路节点指向的入口已停用",
				fmt.Sprintf("第 %d 跳的入口「%s」(#%d) 已停用，这条链路无法建立", step.Position, inbound.Name, inbound.ID), SeverityWarning)
		}
	}
	if step.ExternalOutboundID != nil && *step.ExternalOutboundID > 0 && !c.externalIDs[*step.ExternalOutboundID] {
		report("proxy_path.step.external_missing", "链路节点指向的外部节点已不存在",
			fmt.Sprintf("第 %d 跳引用的外部节点 %d 已被删除", step.Position, *step.ExternalOutboundID), SeverityBlocking)
	}
}

// checkDuplicateDirectPaths surfaces the conflict the topology builder already
// detects. It was only reachable from a failing deployment before.
func (c *collector) checkDuplicateDirectPaths() {
	for _, conflict := range core.DuplicateDirectProxyPathConflicts(c.input.ProxyPaths, c.input.ProxyPathSteps) {
		inboundName := ""
		serverID := int64(0)
		if inbound, ok := c.inboundByID[conflict.InboundID]; ok {
			inboundName = inbound.Name
			serverID = inbound.ServerID
		}
		// The first path keeps the topology; every later duplicate is the
		// redundant one, so the finding is attached there.
		for _, pathID := range conflict.PathIDs[1:] {
			path := c.pathByID[pathID]
			c.add(Finding{
				Code:         "proxy_path.duplicate_direct",
				Severity:     SeverityWarning,
				Scope:        ScopeProxyPath,
				ResourceID:   pathID,
				ResourceName: path.Name,
				ServerID:     serverID,
				Title:        "存在完全重复的直连链路",
				Detail: fmt.Sprintf("入口「%s」下的链路 %v 拓扑完全相同，订阅会出现重复节点",
					inboundName, conflict.PathIDs),
				Remedy: Remedy{Kind: RemedyDelete, Summary: "删除这条重复链路，保留编号最小的一条", Destructive: true},
			})
		}
	}
}
