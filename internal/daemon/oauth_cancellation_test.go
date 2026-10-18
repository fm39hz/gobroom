package daemon

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

// Deliberately returns success after cancellation, as a driver may finish a
// token response concurrently with a user cancelling the authorization.
type lateAuthorizationFixture struct {
	callbackOAuthFixture
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
}

func (f *lateAuthorizationFixture) lateToken(ctx context.Context) (provider.CredentialState, error) {
	close(f.entered)
	<-ctx.Done()
	close(f.cancelled)
	<-f.release
	return oauthFixtureState(provider.OAuthTokenState{AccessToken: "must-not-be-stored", RefreshToken: "late-refresh-token", ClientID: "late-client"})
}

func (f *lateAuthorizationFixture) ExchangeAuthorizationCode(ctx context.Context, _, _ string) (provider.CredentialState, error) {
	return f.lateToken(ctx)
}

func (f *lateAuthorizationFixture) BeginDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{DeviceCode: "private-device", UserCode: "USER", VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (f *lateAuthorizationFixture) WaitForDeviceAuthorization(ctx context.Context, _ provider.DeviceAuthorization) (provider.CredentialState, error) {
	return f.lateToken(ctx)
}

func cancellationAuthDaemon(t *testing.T, flow provider.AuthFlow) (*Daemon, string) {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	node, err := s.CreateProviderNode(store.CreateProviderNodeInput{Name: "OAuth", Prefix: "oauth", BaseURL: "https://provider.test/v1", Protocol: "openai_chat", DefinitionID: "oauth", AuthMode: "oauth2"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.CreateConnection(store.CreateConnectionInput{ProviderNodeID: node.ID, Name: "account", CredentialType: "oauth2", Secret: "original-secret"})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{store: s, server: api.NewServer(s),
		providerAuthFlowIDs: map[extensions.Ref]map[string]string{provider.ProviderDefinitionRef("oauth", 1): {"oauth2": "oauth:oauth2"}},
		authFlows:           map[string]provider.AuthFlow{"oauth:oauth2": flow},
	}
	t.Cleanup(d.cancelAuthorizationSessions)
	return d, connection.ID
}

func awaitAuthorizationSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("authorization did not reach its synchronized lifecycle boundary")
	}
}

type lateDeviceStartFixture struct{ *lateAuthorizationFixture }

func (f lateDeviceStartFixture) BeginDeviceAuthorization(ctx context.Context) (provider.DeviceAuthorization, error) {
	_, _ = f.lateToken(ctx)
	return f.lateAuthorizationFixture.BeginDeviceAuthorization(context.Background())
}

func TestDeviceAuthorizationStartCancelsAndDrainsBeforeLateSuccess(t *testing.T) {
	flow := &lateAuthorizationFixture{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	device := lateDeviceStartFixture{flow}
	driver := deviceAuthorizationDriver(t, device,
		func(ctx context.Context, _ provider.AuthInput) (provider.DeviceAuthorization, error) {
			return device.BeginDeviceAuthorization(ctx)
		},
		device.WaitForDeviceAuthorization,
	)
	d, connectionID := cancellationAuthDaemon(t, driver)
	root, stopRoot := context.WithCancel(context.Background())
	defer stopRoot()
	d.runCtx = root
	defer close(flow.release)
	result := make(chan error, 1)
	go func() {
		_, err := d.startSetupAuthorization(context.Background(), connectionID)
		result <- err
	}()
	awaitAuthorizationSignal(t, flow.entered)
	stopRoot()
	d.cancelAuthorizationSessions()
	awaitAuthorizationSignal(t, flow.cancelled)
	drained := make(chan struct{})
	go func() { d.authWG.Wait(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("device acquisition was omitted from the authorization drain")
	default:
	}
	flow.release <- struct{}{}
	awaitAuthorizationSignal(t, drained)
	if err := <-result; err == nil {
		t.Fatal("late device challenge started polling after shutdown")
	}
	select {
	case <-d.runCtx.Done():
	default:
		t.Fatal("fixture daemon was not cancelled")
	}
	if _, err := d.startSetupAuthorization(context.Background(), connectionID); err == nil {
		t.Fatal("stopped coordinator admitted another device acquisition")
	}
}

func TestAuthorizationCancellationFencesLateSuccessfulToken(t *testing.T) {
	for _, kind := range []string{"authorization_code", "device"} {
		for _, cause := range []string{"session_cancel", "coordinator_stop", "daemon_cancel", "caller_cancel", "session_expiry"} {
			if kind == "device" && (cause == "caller_cancel" || cause == "session_expiry") {
				continue // Device polling intentionally outlives its initiating RPC.
			}
			t.Run(kind+"/"+cause, func(t *testing.T) {
				flow := &lateAuthorizationFixture{callbackOAuthFixture: callbackOAuthFixture{redirectURL: "https://identity.test/callback"}, entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
				d, connectionID := cancellationAuthDaemon(t, flow)
				if kind == "device" {
					driver := deviceAuthorizationDriver(t, flow,
						func(ctx context.Context, _ provider.AuthInput) (provider.DeviceAuthorization, error) {
							return flow.BeginDeviceAuthorization(ctx)
						},
						flow.WaitForDeviceAuthorization,
					)
					d.authFlows["oauth:oauth2"] = driver
				}
				root, stopRoot := context.WithCancel(context.Background())
				defer stopRoot()
				d.runCtx = root
				caller, stopCaller := context.WithCancel(context.Background())
				defer stopCaller()
				// Ensure a failed assertion cannot strand a test worker.
				defer close(flow.release)
				var sessionID string
				result := make(chan error, 1)
				if kind == "authorization_code" {
					challenge, err := d.startAuthorization(connectionID)
					if err != nil {
						t.Fatal(err)
					}
					sessionID = challenge.SessionID
					parsed, _ := url.Parse(challenge.AuthorizationURL)
					go func() {
						_, err := d.completeAuthorization(caller, sessionID, parsed.Query().Get("state"), "approved", "")
						result <- err
					}()
				} else {
					challenge, err := d.startSetupAuthorization(caller, connectionID)
					if err != nil {
						t.Fatal(err)
					}
					sessionID = challenge.SessionID
				}
				awaitAuthorizationSignal(t, flow.entered)
				switch cause {
				case "session_cancel":
					var err error
					if kind == "authorization_code" {
						err = d.cancelAuthorization(sessionID)
					} else {
						if !d.finishSetupAuthorization(sessionID, "cancelled", "authorization was cancelled") {
							err = context.Canceled
						}
					}
					if err != nil {
						t.Fatal(err)
					}
				case "coordinator_stop":
					d.cancelAuthorizationSessions()
				case "daemon_cancel":
					stopRoot()
				case "caller_cancel":
					stopCaller()
				case "session_expiry":
					d.expireAuthorizationSession(sessionID)
				}
				awaitAuthorizationSignal(t, flow.cancelled)
				drained := make(chan struct{})
				go func() { d.authWG.Wait(); close(drained) }()
				select {
				case <-drained:
					t.Fatal("authorization drain returned while a driver was still running")
				default:
				}
				flow.release <- struct{}{}
				awaitAuthorizationSignal(t, drained)
				if kind == "authorization_code" {
					if err := <-result; err == nil {
						t.Fatal("cancelled authorization returned success")
					}
				}
				stored, ok := d.store.ConnectionCredentialByID(connectionID)
				if !ok || stored.Secret != "original-secret" {
					t.Fatal("late successful driver overwrote the connection credential")
				}
				if cause == "session_expiry" {
					status, err := d.authorizationStatus(sessionID)
					if err != nil || status.Status != "expired" {
						t.Fatalf("expiry was overwritten by the returning exchange: %#v %v", status, err)
					}
				}
				if cause == "coordinator_stop" {
					if _, err := d.startAuthorization(connectionID); err == nil {
						t.Fatal("stopped coordinator admitted another authorization")
					}
				}
			})
		}
	}
}
