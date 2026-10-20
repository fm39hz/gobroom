package daemon

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// AuthorizationCallbackIngressConfig enables the optional HTTPS reverse-proxy
// callback endpoint. TLS terminates at the proxy; ListenAddress is private.
type AuthorizationCallbackIngressConfig struct {
	PublicOrigin  string
	ListenAddress string
}

type authorizationCallbackIngress struct {
	origin  *url.URL
	address string
}

var callbackOriginIDNA = idna.New(idna.MapForLookup(), idna.StrictDomainName(true), idna.ValidateLabels(true), idna.VerifyDNSLength(true))

func callbackIngressConfig(config AuthorizationCallbackIngressConfig) (*authorizationCallbackIngress, error) {
	if config.PublicOrigin == "" && config.ListenAddress == "" {
		return nil, nil
	}
	if config.PublicOrigin == "" || config.ListenAddress == "" {
		return nil, fmt.Errorf("callback ingress requires both public origin and listen address")
	}
	origin, err := validateCallbackPublicOrigin(config.PublicOrigin)
	if err != nil {
		return nil, fmt.Errorf("invalid callback public origin: %w", err)
	}
	if err := validateCallbackListenAddress(config.ListenAddress); err != nil {
		return nil, fmt.Errorf("invalid callback listen address: %w", err)
	}
	return &authorizationCallbackIngress{origin: origin, address: config.ListenAddress}, nil
}

func validateCallbackPublicOrigin(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return nil, fmt.Errorf("must be a canonical HTTPS origin")
	}
	for _, r := range raw {
		if r > 0x7f || r < 0x21 {
			return nil, fmt.Errorf("must contain only visible ASCII")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Host == "" || strings.HasSuffix(u.Host, ":") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return nil, fmt.Errorf("must be an HTTPS origin without credentials, query, fragment, or path")
	}
	host := u.Hostname()
	if host == "" || strings.Contains(host, "%") || strings.Contains(host, "*") || strings.HasSuffix(host, ".") {
		return nil, fmt.Errorf("must contain a valid ASCII hostname or IP address")
	}
	for _, r := range host {
		if r > 0x7f || r < 0x21 {
			return nil, fmt.Errorf("must contain only visible ASCII")
		}
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		if ip.Zone() != "" {
			return nil, fmt.Errorf("IP address zones are not allowed")
		}
	} else if _, err := callbackOriginIDNA.ToASCII(host); err != nil {
		return nil, fmt.Errorf("must contain a valid ASCII hostname or IP address")
	}
	if port := u.Port(); port != "" {
		n, parseErr := strconv.Atoi(port)
		if parseErr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return nil, fmt.Errorf("port must be a canonical nonzero numeric port")
		}
	}
	if u.String() != raw && !(u.Path == "/" && strings.TrimSuffix(u.String(), "/") == raw) {
		return nil, fmt.Errorf("must be a canonical HTTPS origin")
	}
	return u, nil
}

func validateCallbackListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("must be an explicit numeric loopback IP host:port")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return fmt.Errorf("must bind to a loopback IP without a zone")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 || strconv.Itoa(n) != port {
		return fmt.Errorf("must use a numeric port from 0 through 65535")
	}
	return nil
}
