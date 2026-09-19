package kernel

type OrderedFallback struct{}

func (OrderedFallback) Plan(_ string, members []MemberRef, _ *StrategyState) []MemberRef {
	return members
}

func (OrderedFallback) OnFailure(failure StrategyFailure, _ *StrategyState) FailureAction {
	if failure.Class == ErrorTerminal {
		return FailureStop
	}
	return FailureContinue
}

// RotatingFallback advances the starting member per node, then retains the
// configured order as fallback. State is independent for every model node.
type RotatingFallback struct{}

func (RotatingFallback) Plan(_ string, members []MemberRef, state *StrategyState) []MemberRef {
	if len(members) < 2 {
		return members
	}
	start := state.Cursor % len(members)
	state.Cursor = (start + 1) % len(members)
	return rotateMembers(members, start)
}

func (RotatingFallback) OnFailure(failure StrategyFailure, _ *StrategyState) FailureAction {
	if failure.Class == ErrorTerminal {
		return FailureStop
	}
	return FailureContinue
}

type WeightedFallback struct{}

func (WeightedFallback) Plan(_ string, members []MemberRef, state *StrategyState) []MemberRef {
	if len(members) < 2 {
		return members
	}
	total := 0
	for _, member := range members {
		weight := member.Weight
		if weight < 1 {
			weight = 1
		}
		total += weight
	}
	point := state.Cursor % total
	chosen := 0
	for i, member := range members {
		weight := member.Weight
		if weight < 1 {
			weight = 1
		}
		if point < weight {
			chosen = i
			break
		}
		point -= weight
	}
	state.Cursor = (state.Cursor + 1) % total
	return rotateMembers(members, chosen)
}

func (WeightedFallback) OnFailure(failure StrategyFailure, _ *StrategyState) FailureAction {
	if failure.Class == ErrorTerminal {
		return FailureStop
	}
	return FailureContinue
}

func rotateMembers(items []MemberRef, start int) []MemberRef {
	result := make([]MemberRef, 0, len(items))
	result = append(result, items[start:]...)
	result = append(result, items[:start]...)
	return result
}
