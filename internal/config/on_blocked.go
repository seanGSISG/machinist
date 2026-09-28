package config

import "fmt"

// OnBlocked describes a bounded backward edge taken when a workflow step is
// blocked.
type OnBlocked struct {
	Goto string `json:"goto"`
	Max  int    `json:"max"`
}

func parseOnBlocked(workflow string, step int, raw any, earlier map[string]bool) (*OnBlocked, error) {
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow %q step %d: on_blocked must be an inline table", workflow, step)
	}
	for key := range value {
		if key != "goto" && key != "max" {
			return nil, fmt.Errorf("workflow %q step %d: on_blocked has unknown field %q", workflow, step, key)
		}
	}
	gotoStep, ok := value["goto"].(string)
	if !ok || gotoStep == "" {
		return nil, fmt.Errorf("workflow %q step %d: on_blocked goto is required", workflow, step)
	}
	if !earlier[gotoStep] {
		return nil, fmt.Errorf("workflow %q step %d: on_blocked goto %q must name an earlier step", workflow, step, gotoStep)
	}
	rawMax, ok := value["max"]
	if !ok {
		return nil, fmt.Errorf("workflow %q step %d: on_blocked max is required", workflow, step)
	}
	max, ok := rawMax.(int64)
	if !ok || max < 1 || uint64(max) > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("workflow %q step %d: on_blocked max must be at least 1", workflow, step)
	}
	return &OnBlocked{Goto: gotoStep, Max: int(max)}, nil
}
