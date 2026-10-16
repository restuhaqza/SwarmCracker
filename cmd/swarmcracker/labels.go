package main

import (
	"fmt"
	"strings"
)

// parseLabelAssignments turns repeated KEY=VALUE flag arguments into a label
// map. Every argument is validated: one without "=" or one whose key is empty
// (or only whitespace) is rejected with an error naming the offending argument,
// so `node update` can never accept a --label-add it would then silently
// ignore. Surrounding whitespace is trimmed off the key and the value first, so
// a padded `--label-add "disk =ssd "` stores the label `disk=ssd` a
// node.labels.disk constraint can match, rather than the unmatchable `"disk "`.
func parseLabelAssignments(pairs []string) (map[string]string, error) {
	labels := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid label %q: expected KEY=VALUE", pair)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("invalid label %q: key must not be empty", pair)
		}
		labels[key] = strings.TrimSpace(value)
	}
	return labels, nil
}

// validateLabelRemovals rejects an empty --label-rm key (or one that is only
// whitespace), naming the offending argument. A key that is well-formed but
// simply not present on the node is not an error: removing it is a no-op (see
// the `node update` long help). Surrounding whitespace is trimmed off a
// well-formed key before removal (see applyLabelChanges), so `--label-rm " disk"`
// removes `disk` rather than silently no-opping on `" disk"`.
func validateLabelRemovals(keys []string) error {
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("invalid label removal %q: key must not be empty", key)
		}
	}
	return nil
}

// validateLabelUpdate is the up-front validation for `node update`: every
// argument is checked before the control client is dialled, so a typo fails
// without any RPC. An invocation that names no label change at all is rejected
// too, rather than performing an empty update.
func validateLabelUpdate(add, remove []string) error {
	if len(add) == 0 && len(remove) == 0 {
		return fmt.Errorf("no label changes requested: pass at least one --label-add or --label-rm")
	}
	if _, err := parseLabelAssignments(add); err != nil {
		return err
	}
	return validateLabelRemovals(remove)
}

// applyLabelChanges returns a new label map with every removal applied first and
// every addition second, so `--label-add k=v --label-rm k` in one invocation
// leaves k=v. Keys and values are trimmed as parseLabelAssignments and
// validateLabelRemovals document, so a padded flag does what a user means. The
// input map is never mutated; removing a key it does not contain is a no-op
// rather than an error.
func applyLabelChanges(current map[string]string, add, remove []string) (map[string]string, error) {
	if err := validateLabelRemovals(remove); err != nil {
		return nil, err
	}
	added, err := parseLabelAssignments(add)
	if err != nil {
		return nil, err
	}

	labels := make(map[string]string, len(current)+len(added))
	for k, v := range current {
		labels[k] = v
	}
	for _, key := range remove {
		delete(labels, strings.TrimSpace(key))
	}
	for k, v := range added {
		labels[k] = v
	}
	return labels, nil
}
