package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
	"golang.org/x/net/http/httpguts"
)

// URL validation describes an intent. This deployment policy determines which
// receiver, if any, owns it; declaring HTTPS never opens a public listener.
func (d *Daemon) authorizationCallbackRoute(target *url.URL, required, dynamic bool) (provider.AuthorizationCallbackMode, string, error) {
	if _, address, ok := loopbackCallbackAddress(target, dynamic); ok {
		return provider.AuthorizationCallbackLoopback, address, nil
	}
	if target != nil && target.Scheme == "https" {
		if ingress := d.callbackIngress; ingress != nil && sameCallbackOrigin(target, ingress.origin) {
			return provider.AuthorizationCallbackProxiedHTTPS, ingress.address, nil
		}
		if !required {
			return provider.AuthorizationCallbackManual, "", nil
		}
	}
	return "", "", fmt.Errorf("callback target has no configured receiver")
}

// Origin equality follows HTTP(S) authority semantics, not redirect-URI string
// equality. The original registered URI is still delivered to the auth driver.
func sameCallbackOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || a.Scheme != b.Scheme || (a.Scheme != "http" && a.Scheme != "https") {
		return false
	}
	port := func(u *url.URL) (int, error) {
		if u.Host == "" || strings.HasSuffix(u.Host, ":") || !httpguts.ValidHostHeader(u.Host) {
			return 0, fmt.Errorf("invalid callback authority")
		}
		if u.Port() == "" {
			if u.Scheme == "https" {
				return 443, nil
			}
			return 80, nil
		}
		n, err := strconv.Atoi(u.Port())
		if err != nil || n < 1 || n > 65535 {
			return 0, fmt.Errorf("invalid callback port")
		}
		return n, nil
	}
	ap, ae := port(a)
	bp, be := port(b)
	if ae != nil || be != nil || ap != bp {
		return false
	}
	aIP, aErr := netip.ParseAddr(a.Hostname())
	bIP, bErr := netip.ParseAddr(b.Hostname())
	if aErr == nil && bErr == nil {
		return aIP == bIP
	}
	return strings.EqualFold(a.Hostname(), b.Hostname())
}

func (d *Daemon) newAuthorizationIngressServer(ctx context.Context, address string) *http.Server {
	server := &http.Server{Addr: address, Handler: http.HandlerFunc(d.handleAuthorizationIngress),
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 35 * time.Second,
		IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}
	server.BaseContext = func(net.Listener) context.Context { return ctx }
	return server
}

func (d *Daemon) handleAuthorizationIngress(w http.ResponseWriter, r *http.Request) {
	d.dispatchAuthorizationCallback(w, r, provider.AuthorizationCallbackProxiedHTTPS)
}
