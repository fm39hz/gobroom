package usage

// DetailOptions selects data classes for an explicitly authorized management
// read. It grants no access by itself; hosts authorize before repository calls.
type DetailOptions struct {
	IncludeExtensions bool `json:"includeExtensions"`
	IncludePrivate    bool `json:"includePrivate"`
	IncludeRaw        bool `json:"includeRaw"`
}
