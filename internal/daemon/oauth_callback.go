package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type oauthCallbackListener struct {
	server   *http.Server
	listener net.Listener
	refs     int
}

func (d *Daemon) acquireOAuthCallbackListener(redirectURI string) (string, string, error) {
	target, err := url.Parse(redirectURI)
	if err != nil {
		return "", "", fmt.Errorf("parse OAuth redirect URI: %w", err)
	}
	key, bindAddress, ok := loopbackCallbackAddress(target)
	if !ok {
		return "", "manual", nil
	}
	d.oauthCallbackMu.Lock()
	if d.oauthCallbacks == nil {
		d.oauthCallbacks = map[string]*oauthCallbackListener{}
	}
	if listener := d.oauthCallbacks[key]; listener != nil {
		listener.refs++
		d.oauthCallbackMu.Unlock()
		return key, "loopback", nil
	}
	listener, err := net.Listen("tcp", bindAddress)
	if err != nil {
		d.oauthCallbackMu.Unlock()
		d.appendLog("warn", fmt.Sprintf("loopback OAuth callback %s unavailable; using manual callback handoff", key))
		return "", "manual", nil
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(d.handleOAuthCallback),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       15 * time.Second,
	}
	d.oauthCallbacks[key] = &oauthCallbackListener{server: server, listener: listener, refs: 1}
	d.oauthCallbackMu.Unlock()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			d.appendLog("warn", "OAuth loopback callback listener stopped")
		}
	}()
	return key, "loopback", nil
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

func (d *Daemon) releaseOAuthCallbackListener(key string) {
	if key == "" {
		return
	}
	d.oauthCallbackMu.Lock()
	listener := d.oauthCallbacks[key]
	if listener == nil {
		d.oauthCallbackMu.Unlock()
		return
	}
	listener.refs--
	if listener.refs > 0 {
		d.oauthCallbackMu.Unlock()
		return
	}
	delete(d.oauthCallbacks, key)
	d.oauthCallbackMu.Unlock()
	_ = listener.listener.Close()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = listener.server.Shutdown(ctx)
	}()
}

func (d *Daemon) closeOAuthCallbackListeners() {
	d.oauthCallbackMu.Lock()
	listeners := d.oauthCallbacks
	d.oauthCallbacks = map[string]*oauthCallbackListener{}
	d.oauthCallbackMu.Unlock()
	for _, listener := range listeners {
		_ = listener.listener.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = listener.server.Shutdown(ctx)
		cancel()
	}
}

func (d *Daemon) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		http.Error(w, "OAuth callback requires GET", http.StatusMethodNotAllowed)
		return
	}
	if !remoteAddressIsLoopback(r.RemoteAddr) {
		http.Error(w, "OAuth callback is loopback-only", http.StatusForbidden)
		return
	}
	callbackURL := (&url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}).String()
	code, state, err := authorizationCallbackCode(callbackURL)
	if err != nil {
		if state != "" && strings.Contains(err.Error(), "rejected") && d.callbackTargetMatchesSession(state, callbackURL) {
			_ = d.cancelAuthorization(state)
		}
		http.Error(w, "Authorization was not completed. Return to GoBroom and try again.", http.StatusBadRequest)
		return
	}
	result, err := d.completeAuthorization(r.Context(), state, state, code, callbackURL)
	if err != nil {
		http.Error(w, "Authorization could not be completed. Return to GoBroom for details.", http.StatusBadRequest)
		return
	}
	_ = result
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Authorization complete. You can close this page and return to GoBroom.\n"))
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
