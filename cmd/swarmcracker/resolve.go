package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/swarmkit/v2/api"
)

// refCandidate is a resolvable cluster entity: a full ID and an optional name.
type refCandidate struct {
	ID   string
	Name string
}

// matchRef resolves a user-supplied reference to exactly one candidate ID.
// Matching order: exact ID, exact name, then a unique ID prefix. It returns a
// descriptive error when nothing matches or the prefix is ambiguous.
func matchRef(ref, kind string, candidates []refCandidate) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("%s reference is required", kind)
	}

	for _, c := range candidates {
		if c.ID == ref {
			return c.ID, nil
		}
	}
	for _, c := range candidates {
		if c.Name != "" && c.Name == ref {
			return c.ID, nil
		}
	}

	var matches []refCandidate
	for _, c := range candidates {
		if strings.HasPrefix(c.ID, ref) {
			matches = append(matches, c)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%s not found: %s", kind, ref)
	case 1:
		return matches[0].ID, nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return "", fmt.Errorf("%s %q is ambiguous: matches %s", kind, ref, strings.Join(ids, ", "))
	}
}

// resolveServiceRef resolves a service reference (full ID, unique ID prefix, or
// name) to its full ID.
func resolveServiceRef(ctx context.Context, client api.ControlClient, ref string) (string, error) {
	resp, err := client.ListServices(ctx, &api.ListServicesRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list services: %w", err)
	}
	candidates := make([]refCandidate, 0, len(resp.Services))
	for _, svc := range resp.Services {
		candidates = append(candidates, refCandidate{ID: svc.ID, Name: svc.Spec.Annotations.Name})
	}
	return matchRef(ref, "service", candidates)
}

// resolveNodeRef resolves a node reference (full ID, unique ID prefix, or
// hostname) to its full ID.
func resolveNodeRef(ctx context.Context, client api.ControlClient, ref string) (string, error) {
	resp, err := client.ListNodes(ctx, &api.ListNodesRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}
	candidates := make([]refCandidate, 0, len(resp.Nodes))
	for _, n := range resp.Nodes {
		name := ""
		if n.Description != nil {
			name = n.Description.Hostname
		}
		candidates = append(candidates, refCandidate{ID: n.ID, Name: name})
	}
	return matchRef(ref, "node", candidates)
}

// resolveTaskRef resolves a task reference (full ID or unique ID prefix) to its
// full ID.
func resolveTaskRef(ctx context.Context, client api.ControlClient, ref string) (string, error) {
	resp, err := client.ListTasks(ctx, &api.ListTasksRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to list tasks: %w", err)
	}
	candidates := make([]refCandidate, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		candidates = append(candidates, refCandidate{ID: t.ID})
	}
	return matchRef(ref, "task", candidates)
}
