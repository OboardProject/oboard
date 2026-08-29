package core

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type FamilyBranchExitBinding struct {
	InterfaceName string
	SourcePrefix  string
}

func IsFamilyBranch(path model.ProxyPath) bool {
	return path.Kind == model.ProxyPathKindFamilyBranch
}

func FamilySplitTemplatePaths(paths []model.ProxyPath, templateID int64) (ipv4, ipv6 model.ProxyPath, err error) {
	if templateID <= 0 {
		return model.ProxyPath{}, model.ProxyPath{}, errors.New("family_split_template_id required")
	}
	for _, path := range paths {
		if path.TemplateID == nil || *path.TemplateID != templateID || !IsFamilyBranch(path) {
			continue
		}
		switch path.Family {
		case model.FamilySplitFamilyIPv4:
			ipv4 = path
		case model.FamilySplitFamilyIPv6:
			ipv6 = path
		}
	}
	if ipv4.ID == 0 || ipv6.ID == 0 {
		return model.ProxyPath{}, model.ProxyPath{}, fmt.Errorf("family split template %d is missing IPv4/IPv6 branches", templateID)
	}
	return ipv4, ipv6, nil
}

func OrderedFamilyBranchSteps(steps []model.ProxyPathStep) []model.ProxyPathStep {
	ordered := append([]model.ProxyPathStep(nil), steps...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Position == ordered[j].Position {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Position < ordered[j].Position
	})
	return ordered
}

func familyBranchStepServerID(step model.ProxyPathStep, inboundByID map[int64]model.Inbound) (int64, bool) {
	if step.NodeType != model.ProxyPathStepServerInbound {
		return 0, false
	}
	if step.InboundID != nil && *step.InboundID > 0 {
		inbound, ok := inboundByID[*step.InboundID]
		if !ok {
			return 0, false
		}
		return inbound.ServerID, true
	}
	if step.ServerID != nil && *step.ServerID > 0 {
		return *step.ServerID, true
	}
	return 0, false
}

func CollapseFamilyBranchSteps(graftServerID int64, steps []model.ProxyPathStep, inboundByID map[int64]model.Inbound) []model.ProxyPathStep {
	ordered := OrderedFamilyBranchSteps(steps)
	if len(ordered) == 0 || graftServerID <= 0 {
		return ordered
	}
	serverID, ok := familyBranchStepServerID(ordered[0], inboundByID)
	if !ok || serverID != graftServerID {
		return ordered
	}
	return ordered[1:]
}

func ParseFamilyBranchExitBinding(raw string) (FamilyBranchExitBinding, error) {
	cfg := parseStepConfig(raw)
	binding := FamilyBranchExitBinding{
		InterfaceName: strings.TrimSpace(stringValue(cfg, "interface_name", "")),
		SourcePrefix:  strings.TrimSpace(stringValue(cfg, "source_prefix", "")),
	}
	if binding.InterfaceName != "" && binding.SourcePrefix != "" {
		return FamilyBranchExitBinding{}, errors.New("family branch last hop cannot bind both interface_name and source_prefix")
	}
	if binding.InterfaceName != "" {
		if err := ValidateNetworkInterfaceName(binding.InterfaceName); err != nil {
			return FamilyBranchExitBinding{}, fmt.Errorf("interface_name: %w", err)
		}
	}
	if binding.SourcePrefix != "" {
		prefix, err := netip.ParsePrefix(binding.SourcePrefix)
		if err != nil {
			return FamilyBranchExitBinding{}, fmt.Errorf("source_prefix must be a valid IPv4 or IPv6 CIDR: %w", err)
		}
		binding.SourcePrefix = prefix.Masked().String()
	}
	return binding, nil
}

func FamilyBranchLastBinding(steps []model.ProxyPathStep) (FamilyBranchExitBinding, error) {
	ordered := OrderedFamilyBranchSteps(steps)
	if len(ordered) == 0 {
		return FamilyBranchExitBinding{}, nil
	}
	return ParseFamilyBranchExitBinding(ordered[len(ordered)-1].ConfigJSON)
}

func ValidateFamilyBranchTransport(steps []model.ProxyPathStep) error {
	ordered := OrderedFamilyBranchSteps(steps)
	for index, step := range ordered {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode == model.ProxyPathTransportPortForward {
			return errors.New("双栈模板不能使用透明端口转发")
		}
		if index == 0 && mode == model.ProxyPathTransportTunnel {
			return errors.New("双栈模板第一跳不能使用隧道")
		}
		binding, err := ParseFamilyBranchExitBinding(step.ConfigJSON)
		if err != nil {
			return err
		}
		if (binding.InterfaceName != "" || binding.SourcePrefix != "") && index != len(ordered)-1 {
			return errors.New("网卡或源前缀绑定只能配置在双栈模板的最后一跳")
		}
		if step.NodeType == model.ProxyPathStepWARP && index != len(ordered)-1 {
			return errors.New("WARP 必须是双栈模板分支的最后一个节点")
		}
	}
	return nil
}

func FamilySplitLocalExitTag(ruleID int64, family string) string {
	return fmt.Sprintf("routing-rule-%d-%s-local", ruleID, family)
}

func FamilySplitBoundExitTag(ruleID int64, family string) string {
	return fmt.Sprintf("routing-rule-%d-%s-bound", ruleID, family)
}

func applyFamilyBranchExitBinding(outbound map[string]any, binding FamilyBranchExitBinding) error {
	if outbound == nil {
		return errors.New("outbound is required")
	}
	if binding.InterfaceName != "" {
		if err := ValidateNetworkInterfaceName(binding.InterfaceName); err != nil {
			return err
		}
		outbound["bind_interface"] = binding.InterfaceName
		return nil
	}
	if binding.SourcePrefix != "" {
		prefix, err := netip.ParsePrefix(binding.SourcePrefix)
		if err != nil {
			return err
		}
		outbound["detour"] = sourcePrefixOutboundTag(prefix.Masked().String())
		delete(outbound, "bind_interface")
	}
	return nil
}
