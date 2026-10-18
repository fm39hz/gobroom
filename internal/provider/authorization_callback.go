package provider

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/fm39hz/gobroom/internal/extensions"
)

var authorizationCallbackName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

// AuthorizationCallback describes a provider callback endpoint without making
// the endpoint or its correlation material part of serialized contracts.
type AuthorizationCallback struct {
	ActionID             string
	URL                  string
	CorrelationParameter string
	CorrelationValue     AuthorizationState
	ResponseParameters   []string
	Required             bool
}

// AuthorizationCallbackInput is delivered through the grant's schema-bound
// input action. The provider interprets its own protocol fields.
type AuthorizationCallbackInput struct {
	URL string `json:"url"`
}

func (AuthorizationCallback) String() string {
	return "[redacted]"
}

func (AuthorizationCallback) GoString() string {
	return "provider.AuthorizationCallback{[redacted]}"
}

func (AuthorizationCallback) MarshalJSON() ([]byte, error) {
	return []byte(`"[redacted]"`), nil
}

// NeedsAllocation reports whether the callback asks the daemon to allocate a
// loopback listener port.
func (c AuthorizationCallback) NeedsAllocation() bool {
	u, err := url.Parse(c.URL)
	if err != nil || u == nil || u.Port() == "" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port == 0
}

// Validate checks static callback properties only. In particular, it never
// resolves hostnames or connects to the callback endpoint.
func (c AuthorizationCallback) Validate() error {
	if c.ActionID == "" || len(c.ActionID) > 128 {
		return fmt.Errorf("authorization callback action ID must contain 1..128 bytes")
	}
	if err := (extensions.Ref{Kind: "action", ID: c.ActionID, ContractVersion: 1}).Validate(); err != nil {
		return fmt.Errorf("authorization callback action ID is invalid")
	}
	if len(c.URL) == 0 || len(c.URL) > 8192 {
		return fmt.Errorf("authorization callback URL has invalid length")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u == nil || !u.IsAbs() || u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.Path == "" {
		return fmt.Errorf("authorization callback URL must be an absolute HTTP URL with a path")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		// localhost is the only permitted DNS name.
	} else {
		ip, parseErr := netip.ParseAddr(host)
		if parseErr != nil || !ip.IsLoopback() || ip.Zone() != "" {
			return fmt.Errorf("authorization callback host must be localhost or a loopback IP")
		}
	}
	portText := u.Port()
	if portText == "" {
		return fmt.Errorf("authorization callback URL requires a fixed port")
	}
	for _, digit := range portText {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("authorization callback URL port is invalid")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("authorization callback URL port is invalid")
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return fmt.Errorf("authorization callback URL requires a valid host and port")
	}

	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) > 16 {
		return fmt.Errorf("authorization callback URL query is invalid")
	}
	for name, values := range query {
		if !authorizationCallbackName.MatchString(name) || len(values) != 1 || len(values[0]) > 2048 {
			return fmt.Errorf("authorization callback URL query parameter is invalid")
		}
	}
	if !authorizationCallbackName.MatchString(c.CorrelationParameter) {
		return fmt.Errorf("authorization callback correlation parameter is invalid")
	}
	if _, exists := query[c.CorrelationParameter]; exists {
		return fmt.Errorf("authorization callback correlation parameter conflicts with URL query")
	}
	encoded := c.CorrelationValue.Encoded()
	if len(encoded) < 32 || len(encoded) > 256 || c.CorrelationValue.Validate() != nil {
		return fmt.Errorf("authorization callback correlation value is invalid")
	}
	if len(c.ResponseParameters) > 16 {
		return fmt.Errorf("authorization callback has too many response parameters")
	}
	seen := make(map[string]struct{}, len(c.ResponseParameters))
	for _, name := range c.ResponseParameters {
		if !authorizationCallbackName.MatchString(name) {
			return fmt.Errorf("authorization callback response parameter is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate authorization callback response parameter")
		}
		if name == c.CorrelationParameter {
			return fmt.Errorf("authorization callback response parameter conflicts with correlation")
		}
		if _, exists := query[name]; exists {
			return fmt.Errorf("authorization callback response parameter conflicts with URL query")
		}
		seen[name] = struct{}{}
	}
	return nil
}
