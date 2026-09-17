package provider

// Profile is a built-in provider preset. Presets define defaults and protocol
// behavior; credentials, models and combos remain user-owned SQLite data.
type Profile struct {
	ID, DisplayName, Protocol, ModelsPath string
	AuthMode                              string
}

var Profiles = map[string]Profile{
	"openai":      {ID: "openai", DisplayName: "OpenAI", Protocol: "openai_chat", ModelsPath: "/v1/models", AuthMode: "api_key"},
	"anthropic":   {ID: "anthropic", DisplayName: "Anthropic", Protocol: "anthropic", ModelsPath: "", AuthMode: "api_key"},
	"openrouter":  {ID: "openrouter", DisplayName: "OpenRouter", Protocol: "openai_chat", ModelsPath: "/models", AuthMode: "api_key"},
	"openai_chat": {ID: "openai_chat", DisplayName: "OpenAI-compatible Chat", Protocol: "openai_chat", ModelsPath: "/models", AuthMode: "api_key"},
	"responses":   {ID: "responses", DisplayName: "OpenAI-compatible Responses", Protocol: "openai_responses", ModelsPath: "/models", AuthMode: "api_key"},
}

func ProfileFor(id string) (Profile, bool) { p, ok := Profiles[id]; return p, ok }
