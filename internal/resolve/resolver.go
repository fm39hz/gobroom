package resolve

import "fmt"

type Member struct{ Ref string }
type Combo struct {
	Name, Strategy string
	Members        []Member
}

type Candidate struct {
	Ref, NodeID, ExternalModel, Protocol string
	Capabilities                         map[string]bool
}

type Catalog interface {
	Resolve(ref string) ([]Candidate, bool)
}

type Resolver struct {
	Models map[string]Candidate
	Combos map[string]Combo
}

func (r *Resolver) Resolve(ref string) ([]Candidate, error) {
	return r.resolve(ref, map[string]bool{})
}

func (r *Resolver) resolve(ref string, stack map[string]bool) ([]Candidate, error) {
	if stack[ref] {
		return nil, fmt.Errorf("combo cycle detected at %q", ref)
	}
	if model, ok := r.Models[ref]; ok {
		return []Candidate{model}, nil
	}
	combo, ok := r.Combos[ref]
	if !ok {
		return nil, fmt.Errorf("unknown route reference %q", ref)
	}
	stack[ref] = true
	defer delete(stack, ref)
	var out []Candidate
	seen := map[string]bool{}
	for _, member := range combo.Members {
		items, err := r.resolve(member.Ref, stack)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			key := item.NodeID + "\x00" + item.ExternalModel
			if !seen[key] {
				seen[key] = true
				out = append(out, item)
			}
		}
	}
	return out, nil
}
