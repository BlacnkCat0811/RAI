package backfill

import "fmt"

// Resume rebuilds the request's block plan and removes blocks whose full
// identity matches a completed block. Unfinished blocks keep the plan-produced
// identity byte for byte. A duplicate, out-of-range or drifted completion (or an
// invalid request) returns conflict; the caller must not publish a mixed plan.
func Resume(req Request, completed []Block) (remaining []Block, conflict bool) {
	plan, err := Plan(req, Facts{})
	if err != nil {
		return nil, true
	}
	byIndex := make(map[int]Block, len(completed))
	for _, done := range completed {
		if done.Index < 0 || done.Index >= len(plan.Blocks) {
			return nil, true
		}
		if _, duplicate := byIndex[done.Index]; duplicate {
			return nil, true
		}
		expected := plan.Blocks[done.Index]
		if done.From != expected.From ||
			done.Through != expected.Through ||
			done.InputHash != expected.InputHash ||
			done.TargetAsOf != expected.TargetAsOf {
			return nil, true
		}
		byIndex[done.Index] = done
	}
	for _, block := range plan.Blocks {
		if _, done := byIndex[block.Index]; !done {
			remaining = append(remaining, block)
		}
	}
	return remaining, false
}

// Status is the pure progress projection of a block list.
type Status struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Complete  int `json:"complete"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	Other     int `json:"other"`
	// Ready is true only when there is at least one block and every block is
	// complete. Intermediate or failed blocks never make an operation ready.
	Ready bool `json:"ready"`
}

// Summarize counts block states without touching storage.
func Summarize(blocks []Block) Status {
	status := Status{Total: len(blocks)}
	for _, block := range blocks {
		switch block.Status {
		case BlockPending:
			status.Pending++
		case BlockRunning:
			status.Running++
		case BlockComplete:
			status.Complete++
		case BlockFailed:
			status.Failed++
		case BlockCancelled:
			status.Cancelled++
		default:
			status.Other++
		}
	}
	status.Ready = status.Total > 0 && status.Complete == status.Total
	return status
}

// Cancel returns a copy with every unfinished block marked cancelled. Completed
// blocks are preserved. It is a pure status change: deleting raw facts,
// receipts or already-published history is explicitly outside this function.
func Cancel(blocks []Block) []Block {
	out := make([]Block, len(blocks))
	copy(out, blocks)
	for i := range out {
		if out[i].Status != BlockComplete {
			out[i].Status = BlockCancelled
		}
	}
	return out
}

// ValidateCompleted checks that a completed block is internally well-formed.
// It is intentionally independent of any request; Resume performs identity
// matching against the request.
func ValidateCompleted(block Block) error {
	if block.Index < 0 {
		return fmt.Errorf("%w: negative block index", ErrInvalidRequest)
	}
	if _, err := parseDay(block.From); err != nil {
		return fmt.Errorf("%w: block from must be YYYY-MM-DD", ErrInvalidRequest)
	}
	if _, err := parseDay(block.Through); err != nil {
		return fmt.Errorf("%w: block through must be YYYY-MM-DD", ErrInvalidRequest)
	}
	if len(block.InputHash) != 64 {
		return fmt.Errorf("%w: block inputHash must be 64 hex characters", ErrInvalidRequest)
	}
	if block.TargetAsOf == "" {
		return fmt.Errorf("%w: block targetAsOf is required", ErrInvalidRequest)
	}
	return nil
}
