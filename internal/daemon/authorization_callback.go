package daemon

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fm39hz/gobroom/internal/provider"
)

type AuthorizationCallbackMode = provider.AuthorizationCallbackMode

const (
	AuthorizationCallbackLoopback     = provider.AuthorizationCallbackLoopback
	AuthorizationCallbackManual       = provider.AuthorizationCallbackManual
	AuthorizationCallbackProxiedHTTPS = provider.AuthorizationCallbackProxiedHTTPS
)

type AuthorizationCallbackView struct {
	ActionID string                    `json:"actionId"`
	Mode     AuthorizationCallbackMode `json:"mode"`
}

type authorizationCallbackKey struct{ owner, action string }

type authorizationCallbackBinding struct {
	spec        provider.AuthorizationCallback
	invoke      func(context.Context, string) error
	target      *url.URL
	fixed       url.Values
	allowed     map[string]bool
	bindAddress string
	mode        provider.AuthorizationCallbackMode
}

type authorizationCallbackListener struct {
	server   *http.Server
	listener net.Listener
}

func loopbackCallbackAddress(target *url.URL, allowDynamic bool) (key, bindAddress string, ok bool) {
	if target == nil || target.Scheme != "http" || target.User != nil || target.Host == "" || target.Port() == "" || target.Fragment != "" || target.Path == "" {
		return "", "", false
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil || port < 0 || (port == 0 && !allowDynamic) || port > 65535 {
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
	canonical := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	return canonical, canonical, true
}

func remoteAddressIsLoopback(raw string) bool {
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Replacing one owner's grants is atomic with respect to dispatch. New listeners
// are acquired before publication; failure cannot dismantle the previous grants.
// Registry locks never span driver invocation or HTTP shutdown.
func (d *Daemon) replaceAuthorizationCallbacks(owner string, bindings []authorizationCallbackBinding) ([]AuthorizationCallbackView, error) {
	if owner == "" || len(bindings) > 8 {
		return nil, fmt.Errorf("invalid callback grant owner or count")
	}
	compiled := make(map[authorizationCallbackKey]authorizationCallbackBinding, len(bindings))
	for _, binding := range bindings {
		if binding.invoke == nil || binding.spec.Validate() != nil || binding.spec.NeedsAllocation() {
			return nil, fmt.Errorf("invalid callback grant")
		}
		key := authorizationCallbackKey{owner: owner, action: binding.spec.ActionID}
		if _, exists := compiled[key]; exists {
			return nil, fmt.Errorf("duplicate callback grant")
		}
		target, _ := url.Parse(binding.spec.URL)
		mode, address, err := d.authorizationCallbackRoute(target, binding.spec.Required, false)
		if err != nil {
			return nil, err
		}
		fixed, _ := url.ParseQuery(target.RawQuery)
		binding.target, binding.fixed, binding.bindAddress, binding.mode = target, fixed, address, mode
		binding.spec.ResponseParameters = append([]string(nil), binding.spec.ResponseParameters...)
		binding.allowed = make(map[string]bool, len(binding.spec.ResponseParameters))
		for _, name := range binding.spec.ResponseParameters {
			binding.allowed[name] = true
		}
		compiled[key] = binding
	}
	d.callbackMu.Lock()
	if d.callbacksClosed {
		d.callbackMu.Unlock()
		return nil, fmt.Errorf("callback registry is stopped")
	}
	if d.callbackListeners == nil {
		d.callbackListeners = make(map[string]*authorizationCallbackListener)
	}
	if d.callbackBindings == nil {
		d.callbackBindings = make(map[authorizationCallbackKey]authorizationCallbackBinding)
	}
	for key, binding := range compiled {
		if reservation, exists := d.callbackReservations[key]; exists && !sameCallbackGrant(reservation.resolved, binding.spec) {
			d.callbackMu.Unlock()
			return nil, fmt.Errorf("callback activation does not match its reservation")
		}
		for reservedKey, reservation := range d.callbackReservations {
			if reservedKey.owner == owner && reservedKey.action != key.action && reservation.mode == AuthorizationCallbackLoopback && reservation.bindAddress == binding.bindAddress {
				if _, owns := d.callbackReservations[key]; !owns {
					d.callbackMu.Unlock()
					return nil, fmt.Errorf("callback resource belongs to another action")
				}
			}
		}
	}
	created := make(map[string]*authorizationCallbackListener)
	views := make([]AuthorizationCallbackView, 0, len(bindings))
	for _, source := range bindings {
		key := authorizationCallbackKey{owner: owner, action: source.spec.ActionID}
		binding := compiled[key]
		mode := binding.mode
		reservation, reserved := d.callbackReservations[key]
		if mode == AuthorizationCallbackManual || (reserved && reservation.mode == AuthorizationCallbackManual) {
			delete(compiled, key)
			views = append(views, AuthorizationCallbackView{ActionID: source.spec.ActionID, Mode: AuthorizationCallbackManual})
			continue
		}
		if mode == AuthorizationCallbackProxiedHTTPS {
			views = append(views, AuthorizationCallbackView{ActionID: source.spec.ActionID, Mode: mode})
			continue
		}
		if d.callbackListeners[binding.bindAddress] == nil && created[binding.bindAddress] == nil {
			listener, err := net.Listen("tcp", binding.bindAddress)
			if err != nil {
				if binding.spec.Required {
					for _, allocated := range created {
						_ = allocated.listener.Close()
					}
					d.callbackMu.Unlock()
					return nil, fmt.Errorf("required callback listener is unavailable")
				}
				mode = AuthorizationCallbackManual
				delete(compiled, key)
			} else {
				server := d.newAuthorizationCallbackServer()
				created[binding.bindAddress] = &authorizationCallbackListener{server: server, listener: listener}
			}
		}
		views = append(views, AuthorizationCallbackView{ActionID: source.spec.ActionID, Mode: mode})
	}
	for key := range d.callbackBindings {
		if key.owner == owner {
			delete(d.callbackBindings, key)
		}
	}
	for key := range d.callbackReservations {
		if key.owner == owner {
			delete(d.callbackReservations, key)
		}
	}
	for key, binding := range compiled {
		d.callbackBindings[key] = binding
	}
	for address, listener := range created {
		d.callbackListeners[address] = listener
	}
	retired := d.pruneAuthorizationCallbackListenersLocked()
	d.callbackMu.Unlock()
	for _, listener := range retired {
		shutdownCallbackListener(listener)
	}
	for _, listener := range created {
		d.serveAuthorizationCallback(listener)
	}
	return views, nil
}

func (d *Daemon) newAuthorizationCallbackServer() *http.Server {
	return &http.Server{Handler: http.HandlerFunc(d.handleAuthorizationCallback), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}
}

func (d *Daemon) serveAuthorizationCallback(listener *authorizationCallbackListener) {
	go func() {
		if err := listener.server.Serve(listener.listener); err != nil && err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) {
			d.appendLog("warn", "authorization callback listener stopped")
		}
	}()
}

func (d *Daemon) pruneAuthorizationCallbackListenersLocked() []*authorizationCallbackListener {
	used := make(map[string]bool, len(d.callbackBindings))
	for _, binding := range d.callbackBindings {
		used[binding.bindAddress] = true
	}
	for _, reservation := range d.callbackReservations {
		if reservation.mode == AuthorizationCallbackLoopback {
			used[reservation.bindAddress] = true
		}
	}
	var retired []*authorizationCallbackListener
	for address, listener := range d.callbackListeners {
		if used[address] {
			continue
		}
		// Close the actual reservation before another registration can bind it.
		_ = listener.listener.Close()
		delete(d.callbackListeners, address)
		retired = append(retired, listener)
	}
	return retired
}

func shutdownCallbackListener(listener *authorizationCallbackListener) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = listener.server.Shutdown(ctx)
	}()
}

func (d *Daemon) releaseAuthorizationCallbacks(owner string) {
	if owner == "" {
		return
	}
	d.callbackMu.Lock()
	for key := range d.callbackBindings {
		if key.owner == owner {
			delete(d.callbackBindings, key)
		}
	}
	for key := range d.callbackReservations {
		if key.owner == owner {
			delete(d.callbackReservations, key)
		}
	}
	retired := d.pruneAuthorizationCallbackListenersLocked()
	d.callbackMu.Unlock()
	for _, listener := range retired {
		shutdownCallbackListener(listener)
	}
}

func (d *Daemon) closeAuthorizationCallbacks() {
	d.callbackMu.Lock()
	d.callbacksClosed = true
	d.callbackBindings = nil
	d.callbackReservations = nil
	retired := d.pruneAuthorizationCallbackListenersLocked()
	d.callbackMu.Unlock()
	for _, listener := range retired {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = listener.server.Shutdown(ctx)
		cancel()
	}
}

func sameCallbackGrant(a, b provider.AuthorizationCallback) bool {
	return a.ActionID == b.ActionID && a.URL == b.URL && a.CorrelationParameter == b.CorrelationParameter && a.CorrelationValue.Encoded() == b.CorrelationValue.Encoded() && a.Required == b.Required && slices.Equal(a.ResponseParameters, b.ResponseParameters)
}

func (binding authorizationCallbackBinding) matches(actual *url.URL, query url.Values) bool {
	want := binding.target
	if want == nil || !sameCallbackOrigin(actual, want) || actual.EscapedPath() != want.EscapedPath() {
		return false
	}
	for name, values := range binding.fixed {
		got := query[name]
		if len(got) != 1 || got[0] != values[0] {
			return false
		}
	}
	correlation := query[binding.spec.CorrelationParameter]
	if len(correlation) != 1 || subtle.ConstantTimeCompare([]byte(correlation[0]), []byte(binding.spec.CorrelationValue.Encoded())) != 1 {
		return false
	}
	for name, values := range query {
		if len(values) != 1 || len(values[0]) > 4096 {
			return false
		}
		if name == binding.spec.CorrelationParameter {
			continue
		}
		if _, fixed := binding.fixed[name]; !fixed && !binding.allowed[name] {
			return false
		}
	}
	return true
}

func (d *Daemon) handleAuthorizationCallback(w http.ResponseWriter, r *http.Request) {
	d.dispatchAuthorizationCallback(w, r, provider.AuthorizationCallbackLoopback)
}

func (d *Daemon) dispatchAuthorizationCallback(w http.ResponseWriter, r *http.Request, mode provider.AuthorizationCallbackMode) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		http.Error(w, "Callback requires GET", http.StatusMethodNotAllowed)
		return
	}
	if !remoteAddressIsLoopback(r.RemoteAddr) {
		http.Error(w, "Callback is loopback-only", http.StatusForbidden)
		return
	}
	scheme := "http"
	if mode == provider.AuthorizationCallbackProxiedHTTPS {
		if d.callbackIngress == nil || !sameCallbackOrigin(&url.URL{Scheme: "https", Host: r.Host}, d.callbackIngress.origin) {
			http.Error(w, "Callback origin is not configured", http.StatusBadRequest)
			return
		}
		scheme = "https" // configured TLS terminator, never an untrusted proxy header
	}
	actual := &url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	encoded := actual.String()
	query, err := url.ParseQuery(actual.RawQuery)
	if err != nil || len(encoded) > 8192 || len(query) > 33 {
		http.Error(w, "Invalid callback", http.StatusBadRequest)
		return
	}
	d.callbackMu.Lock()
	var selected authorizationCallbackBinding
	matches := 0
	for _, binding := range d.callbackBindings {
		if binding.mode == mode && binding.matches(actual, query) {
			selected = binding
			matches++
		}
	}
	d.callbackMu.Unlock()
	if matches != 1 {
		http.Error(w, "Callback does not match an active grant", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), authorizationStepTimeout)
	defer cancel()
	// Preserve the issuer-registered target spelling (including an explicit
	// default port). Only the already validated response query is supplied by
	// the request; proxy headers never reconstruct an authorization target.
	delivered := *selected.target
	delivered.RawQuery = actual.RawQuery
	if err := selected.invoke(ctx, delivered.String()); err != nil {
		http.Error(w, "Authorization could not be completed. Return to GoBroom for details.", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Authorization response received. Return to GoBroom to check status and activation.\n"))
}
