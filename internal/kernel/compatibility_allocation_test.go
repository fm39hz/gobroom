package kernel

import "testing"

func TestComposeCompatibilityPlanOwnsNestedLossEvidence(t *testing.T) {
	requested := map[string]any{"nested": map[string]any{"values": []any{"asked"}}}
	effective := map[string]any{"nested": map[string]any{"values": []any{"used"}}}
	input := [][]FacetMapping{{{
		Facet: "reasoning.budget", Paths: []string{"request.reasoning.budget"}, Disposition: FacetDegraded,
		Losses: []LossRecord{{ID: "reasoning.clamped", Requested: requested, Effective: effective, SemanticPaths: []string{"request.reasoning.budget"}}},
	}}}
	policy := CompatibilityPolicy{
		RequiredFacets: []string{"reasoning.budget"}, AllowedLosses: []string{"reasoning.clamped"},
		LossSources: map[string][]string{"reasoning.clamped": {"route.combo"}},
	}
	plan := ComposeCompatibilityPlan(input, policy)
	if !plan.Supported || len(plan.Mappings) != 1 || len(plan.Losses) != 1 {
		t.Fatalf("unexpected composed plan: %#v", plan)
	}

	requested["nested"].(map[string]any)["values"].([]any)[0] = "input-mutated"
	effective["nested"].(map[string]any)["values"].([]any)[0] = "input-mutated"
	input[0][0].Paths[0] = "input-mutated"
	input[0][0].Losses[0].SemanticPaths[0] = "input-mutated"
	policy.LossSources["reasoning.clamped"][0] = "input-mutated"
	assertLossValue := func(name string, value any, want string) {
		t.Helper()
		got := value.(map[string]any)["nested"].(map[string]any)["values"].([]any)[0]
		if got != want {
			t.Fatalf("%s value changed through input alias: got %v want %q", name, got, want)
		}
	}
	assertLossValue("mapping requested", plan.Mappings[0].Losses[0].Requested, "asked")
	assertLossValue("mapping effective", plan.Mappings[0].Losses[0].Effective, "used")
	assertLossValue("plan requested", plan.Losses[0].Requested, "asked")
	assertLossValue("plan effective", plan.Losses[0].Effective, "used")
	if plan.Mappings[0].Paths[0] != "request.reasoning.budget" || plan.Mappings[0].Losses[0].SemanticPaths[0] != "request.reasoning.budget" || plan.Mappings[0].Losses[0].PolicySources[0] != "route.combo" {
		t.Fatalf("composed mapping aliases caller data: %#v", plan.Mappings[0])
	}

	plan.Mappings[0].Losses[0].Requested.(map[string]any)["nested"].(map[string]any)["values"].([]any)[0] = "mapping-mutated"
	plan.Mappings[0].Losses[0].Effective.(map[string]any)["nested"].(map[string]any)["values"].([]any)[0] = "mapping-mutated"
	plan.Mappings[0].Paths[0] = "output-mutated"
	assertLossValue("plan requested after mapping mutation", plan.Losses[0].Requested, "asked")
	assertLossValue("plan effective after mapping mutation", plan.Losses[0].Effective, "used")
	if input[0][0].Paths[0] != "input-mutated" {
		t.Fatal("composed mapping aliases caller's path slice")
	}
	plan.Losses[0].Requested.(map[string]any)["nested"].(map[string]any)["values"].([]any)[0] = "plan-mutated"
	assertLossValue("mapping requested after plan mutation", plan.Mappings[0].Losses[0].Requested, "mapping-mutated")
	assertLossValue("input requested after output mutation", input[0][0].Losses[0].Requested, "input-mutated")
	assertLossValue("input effective after output mutation", input[0][0].Losses[0].Effective, "input-mutated")
}

func BenchmarkComposeCompatibilityPlan(b *testing.B) {
	for _, benchmark := range []struct {
		name  string
		lossy bool
	}{
		{name: "no-loss"},
		{name: "named-loss", lossy: true},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			reports := make([][]FacetMapping, 3)
			for stage := range reports {
				reports[stage] = make([]FacetMapping, 0, 6)
				for facet := 0; facet < 6; facet++ {
					mapping := FacetMapping{
						Facet: "facet." + string(rune('a'+facet)),
						Paths: []string{"request.path", "upstream.path"}, Disposition: FacetTranslated,
					}
					if benchmark.lossy && facet == 2 && stage == 1 {
						mapping.Disposition = FacetDegraded
						mapping.Losses = []LossRecord{{
							ID: "reasoning.clamped", Requested: map[string]any{"effort": "xhigh"}, Effective: map[string]any{"effort": "high"},
							SemanticPaths: []string{"request.reasoning.effort"},
						}}
					}
					reports[stage] = append(reports[stage], mapping)
				}
			}
			policy := CompatibilityPolicy{RequiredFacets: []string{"facet.a", "facet.b", "facet.c", "facet.d", "facet.e", "facet.f"}}
			if benchmark.lossy {
				policy.AllowedLosses = []string{"reasoning.clamped"}
				policy.LossSources = map[string][]string{"reasoning.clamped": {"model.combo"}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = ComposeCompatibilityPlan(reports, policy)
			}
		})
	}
}
