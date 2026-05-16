package engine

import (
	"sort"

	pb "github.com/pacepoker/poker/gen/go/poker/v1"
)

// pot holds a single pot's amount and the set of players eligible to win it.
type pot struct {
	amount      int64
	eligibleIDs []string // in ActionOrder order
}

// computePots builds the main pot and all side pots from each player's
// TotalCommittedThisHand. Folded players contribute chips but are excluded
// from eligible winners.
//
// The algorithm sorts all contributors by commitment level ascending, then
// for each new level computes the amount contributed by all remaining players
// and the subset of non-folded players eligible to win that pot.
//
// Consecutive pots with identical eligible sets are merged so that the result
// has at most one pot per unique eligible group.
//
// Returns nil when no player has committed any chips (e.g. synthetic test states
// that don't populate TotalCommittedThisHand — callers should fall back to a
// single pot with h.Pot).
func computePots(state *pb.GameState) []pot {
	type entry struct {
		id        string
		committed int64
		folded    bool
	}

	var entries []entry
	for id, p := range state.Players {
		if p.TotalCommittedThisHand > 0 {
			entries = append(entries, entry{id, p.TotalCommittedThisHand, p.IsFolded})
		}
	}
	if len(entries) == 0 {
		return nil
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].committed < entries[j].committed
	})

	// Build eligible lookup in ActionOrder sequence so pots are deterministic.
	actionOrder := []string{}
	if state.CurrentHand != nil {
		actionOrder = state.CurrentHand.ActionOrder
	}

	eligibleInOrder := func(from []entry) []string {
		set := make(map[string]bool, len(from))
		for _, e := range from {
			if !e.folded {
				set[e.id] = true
			}
		}
		var out []string
		for _, pid := range actionOrder {
			if set[pid] {
				out = append(out, pid)
			}
		}
		// Include any eligible player not in ActionOrder (shouldn't happen in practice).
		inOrder := make(map[string]bool, len(out))
		for _, pid := range out {
			inOrder[pid] = true
		}
		for _, e := range from {
			if !e.folded && !inOrder[e.id] {
				out = append(out, e.id)
			}
		}
		return out
	}

	var raw []pot
	prev := int64(0)
	n := len(entries)

	for i, e := range entries {
		if e.committed <= prev {
			continue // tie — already handled at prev level
		}
		level := e.committed
		amount := (level - prev) * int64(n-i)
		eligible := eligibleInOrder(entries[i:])
		if amount > 0 {
			raw = append(raw, pot{amount: amount, eligibleIDs: eligible})
		}
		prev = level
	}

	// Merge consecutive pots that share an identical eligible set.
	if len(raw) == 0 {
		return nil
	}
	merged := []pot{raw[0]}
	for _, p := range raw[1:] {
		last := &merged[len(merged)-1]
		if sameStringSlice(last.eligibleIDs, p.eligibleIDs) {
			last.amount += p.amount
		} else {
			merged = append(merged, p)
		}
	}
	return merged
}

// computeRake returns the rake owed on a given pot amount according to config.
// Returns 0 if no rake is configured.
func computeRake(amount int64, cfg *pb.CashGameConfig) int64 {
	if cfg == nil || cfg.RakeBps <= 0 || amount <= 0 {
		return 0
	}
	rake := amount * int64(cfg.RakeBps) / 10000
	if cfg.RakeCap > 0 && rake > cfg.RakeCap {
		rake = cfg.RakeCap
	}
	return rake
}

// firstEligibleInOrder returns the first non-folded, eligible player in ActionOrder.
func firstEligibleInOrder(state *pb.GameState, eligibleIDs []string) string {
	if state.CurrentHand == nil {
		return ""
	}
	eligible := make(map[string]bool, len(eligibleIDs))
	for _, id := range eligibleIDs {
		eligible[id] = true
	}
	for _, pid := range state.CurrentHand.ActionOrder {
		p := state.Players[pid]
		if p != nil && !p.IsFolded && eligible[pid] {
			return pid
		}
	}
	return ""
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}