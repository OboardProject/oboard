package core

import (
	"errors"
	"fmt"

	"github.com/OboardProject/oboard/internal/model"
)

type topologyEdge struct {
	source int64
	target int64
}

func ValidateTopologyDAG(servers []model.Server, forwards []model.PortForward, tunnels []model.Tunnel) error {
	known := make(map[int64]bool, len(servers))
	for _, server := range servers {
		known[server.ID] = true
	}
	edges := make([]topologyEdge, 0, len(forwards)+len(tunnels))
	for _, forward := range forwards {
		if forward.Enabled && forward.TargetServerID != 0 {
			edges = append(edges, topologyEdge{source: forward.SourceServerID, target: forward.TargetServerID})
		}
	}
	for _, tunnel := range tunnels {
		if tunnel.Enabled {
			edges = append(edges, topologyEdge{source: tunnel.SourceServerID, target: tunnel.TargetServerID})
		}
	}
	graph := make(map[int64][]int64, len(known))
	for _, edge := range edges {
		if edge.source == edge.target {
			return errors.New("topology source and target must differ")
		}
		if !known[edge.source] {
			return fmt.Errorf("source server %d does not exist", edge.source)
		}
		if !known[edge.target] {
			return fmt.Errorf("target server %d does not exist", edge.target)
		}
		graph[edge.source] = append(graph[edge.source], edge.target)
	}
	visiting := map[int64]bool{}
	visited := map[int64]bool{}
	var visit func(int64) error
	visit = func(node int64) error {
		if visiting[node] {
			return errors.New("topology graph contains a cycle")
		}
		if visited[node] {
			return nil
		}
		visiting[node] = true
		for _, next := range graph[node] {
			if err := visit(next); err != nil {
				return err
			}
		}
		visiting[node] = false
		visited[node] = true
		return nil
	}
	for id := range known {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
