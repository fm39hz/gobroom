package kernel

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// CredentialPresentation is provider-manifest data describing how a resolved
// connection secret is presented on an HTTP request. Static headers are
// protocol metadata only; secrets remain in the connection credential store.
type CredentialPresentation struct {
	Header  string            `json:"header,omitempty"`
	Prefix  string            `json:"prefix,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (p CredentialPresentation) Validate() error {
	if p.Header != "" && (!validCredentialHeader(p.Header) || restrictedRequestHeader(p.Header)) {
		return fmt.Errorf("credential presentation token header %q is invalid", p.Header)
	}
	if p.Header == "" && p.Prefix != "" {
		return fmt.Errorf("credential presentation prefix requires a header")
	}
	seen := make(map[string]struct{}, len(p.Headers))
	for name := range p.Headers {
		if !validCredentialHeader(name) || reservedStaticCredentialHeader(name) || strings.EqualFold(name, p.Header) {
			return fmt.Errorf("credential presentation static header %q is invalid, reserved or duplicates the token header", name)
		}
		canonical := http.CanonicalHeaderKey(name)
		if _, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("credential presentation static header %q is duplicated with different casing", name)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func validCredentialHeader(name string) bool {
	if strings.TrimSpace(name) != name || name == "" || http.CanonicalHeaderKey(name) == "" {
		return false
	}
	for _, char := range name {
		if char <= 32 || char >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", char) {
			return false
		}
	}
	return true
}

func reservedStaticCredentialHeader(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "authorization":
		return true
	}
	if restrictedRequestHeader(name) {
		return true
	}
	normalized := strings.NewReplacer("-", "", "_", "").Replace(lower)
	for _, sensitive := range []string{"authorization", "auth", "apikey", "token", "secret", "cookie"} {
		if strings.Contains(normalized, sensitive) {
			return true
		}
	}
	return false
}

func restrictedRequestHeader(name string) bool {
	switch strings.ToLower(name) {
	case "proxy-authorization", "cookie", "set-cookie", "host", "content-length", "content-type", "transfer-encoding":
		return true
	default:
		return false
	}
}

// ApplyCredentialPresentation applies non-secret protocol headers and then
// places the credential secret in the declared header. The caller owns headers.
func ApplyCredentialPresentation(headers http.Header, credential Credential, presentation CredentialPresentation) error {
	if headers == nil {
		return fmt.Errorf("credential presentation requires an initialized header map")
	}
	if err := credential.ValidateRuntimeContext(); err != nil {
		return err
	}
	if err := presentation.Validate(); err != nil {
		return err
	}
	names := make([]string, 0, len(presentation.Headers))
	for name := range presentation.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		headers.Set(name, presentation.Headers[name])
	}
	for name, values := range credential.RequestHeaders {
		headers[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	if credential.Secret != "" {
		if presentation.Header == "" {
			return fmt.Errorf("credential secret has no declared presentation header")
		}
		headers.Set(presentation.Header, presentation.Prefix+credential.Secret)
	}
	return nil
}

func validCredentialHeaderValue(value string) bool {
	for _, char := range value {
		if char == '\r' || char == '\n' || char == 0 || (char < 32 && char != '\t') || char == 127 {
			return false
		}
	}
	return true
}

func (c Credential) ValidateRuntimeContext() error {
	if err := c.ValidateAuthContext(); err != nil {
		return err
	}
	if len(c.RequestHeaders) > maxAuthContextEntries {
		return fmt.Errorf("credential request headers exceed %d entries", maxAuthContextEntries)
	}
	total := 0
	for name, values := range c.RequestHeaders {
		if !validCredentialHeader(name) || restrictedRequestHeader(name) {
			return fmt.Errorf("auth flow returned invalid or reserved request header %q", name)
		}
		if len(values) > 16 {
			return fmt.Errorf("auth flow returned too many values for request header %q", name)
		}
		for _, value := range values {
			if !validCredentialHeaderValue(value) {
				return fmt.Errorf("auth flow returned invalid value for request header %q", name)
			}
			total += len(name) + len(value)
			if total > maxAuthContextBytes {
				return fmt.Errorf("credential request headers exceed %d bytes", maxAuthContextBytes)
			}
		}
	}
	return nil
}
