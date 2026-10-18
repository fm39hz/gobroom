package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
)

// The code flow owns OAuth interpretation, but not a second listener runtime.
func (d *Daemon) acquireOAuthCallbackListener(redirectURI, state string) (string, string, error) {
	target, err := url.Parse(redirectURI)
	if err != nil {
		return "", "", fmt.Errorf("invalid OAuth redirect URI")
	}
	if _, _, ok := loopbackCallbackAddress(target); !ok {
		return "", "manual", nil
	}
	correlation, err := provider.NewAuthorizationState(state)
	if err != nil {
		return "", "", fmt.Errorf("invalid authorization correlation")
	}
	binding := authorizationCallbackBinding{
		spec: provider.AuthorizationCallback{ActionID: "authorization-code", URL: redirectURI, CorrelationParameter: "state", CorrelationValue: correlation, ResponseParameters: []string{"code", "error", "error_description", "error_uri"}},
		invoke: func(ctx context.Context, callbackURL string) error {
			code, receivedState, err := authorizationCallbackCode(callbackURL)
			if err != nil {
				if errors.Is(err, errOAuthAuthorizationDenied) {
					if denialErr := d.denyAuthorization(state, receivedState, callbackURL); denialErr != nil {
						return denialErr
					}
				}
				return err
			}
			_, err = d.completeAuthorization(ctx, state, receivedState, code, callbackURL)
			return err
		},
	}
	views, err := d.replaceAuthorizationCallbacks(state, []authorizationCallbackBinding{binding})
	if err != nil {
		return "", "", err
	}
	if len(views) == 1 && views[0].Mode == AuthorizationCallbackLoopback {
		return state, "loopback", nil
	}
	return "", "manual", nil
}

func loopbackCallbackAddress(target *url.URL) (key, bindAddress string, ok bool) {
	if target == nil || target.Scheme != "http" || target.User != nil || target.Host == "" || target.Port() == "" || target.Fragment != "" || target.Path == "" {
		return "", "", false
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", "", false
	}
	host := strings.TrimSuffix(strings.ToLower(target.Hostname()), ".")
	if host == "localhost" {
		return net.JoinHostPort(host, strconv.Itoa(port)), net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), true
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", "", false
	}
	canonicalHost := ip.String()
	return net.JoinHostPort(canonicalHost, strconv.Itoa(port)), net.JoinHostPort(canonicalHost, strconv.Itoa(port)), true
}

func (d *Daemon) callbackTargetMatchesSession(sessionID, callbackURL string) bool {
	sessions := d.authorizationSessions()
	d.pruneAuthorizationSessions(sessions, time.Now())
	sessions.mu.Lock()
	session, exists := sessions.byID[sessionID]
	match := exists && session.kind == "authorization_code" && session.status == "awaiting_user" && sameOAuthCallbackTarget(session.redirectURL, callbackURL)
	sessions.mu.Unlock()
	return match
}

func remoteAddressIsLoopback(raw string) bool {
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
