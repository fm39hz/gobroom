package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/fm39hz/gobroom/internal/api"
	"github.com/fm39hz/gobroom/internal/extensions"
	"github.com/fm39hz/gobroom/internal/kernel"
	"github.com/fm39hz/gobroom/internal/provider"
	"github.com/fm39hz/gobroom/internal/store"
)

// generationDeviceFlow returns an OAuth-shaped credential only after the test
// has changed the connection while device polling is in flight.
type generationDeviceFlow struct {
	entered chan struct{}
	release chan struct{}
}

func (*generationDeviceFlow) ID() string                        { return "device-generation-fixture" }
func (*generationDeviceFlow) SetupSchema() provider.SetupSchema { return provider.SetupSchema{} }
func (*generationDeviceFlow) Resolve(_ context.Context, input provider.AuthInput) (kernel.Credential, error) {
	return kernel.Credential{ConnectionID: input.ConnectionID, Type: input.Type, Secret: input.Secret}, nil
}
func (*generationDeviceFlow) Refresh(_ context.Context, _ provider.AuthInput, credential kernel.Credential) (provider.CredentialState, error) {
	return provider.NewCredentialState(credential.Secret)
}
func (*generationDeviceFlow) BeginDeviceAuthorization(context.Context) (provider.DeviceAuthorization, error) {
	return provider.DeviceAuthorization{
		DeviceCode: "source-device-code", UserCode: "SOURCE-CODE",
		VerificationURL: "https://identity.test/device", ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}
func (f *generationDeviceFlow) WaitForDeviceAuthorization(ctx context.Context, _ provider.DeviceAuthorization) (provider.CredentialState, error) {
	close(f.entered)
	select {
	case <-f.release:
		return oauthFixtureState(provider.OAuthTokenState{AccessToken: "delayed-device-token", RefreshToken: "delayed-refresh-token", ClientID: "device-client"})
	case <-ctx.Done():
		return provider.CredentialState{}, ctx.Err()
	}
}

func TestDeviceAuthorizationDoesNotOverwriteChangedConnectionGeneration(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *store.Store, string)
		check  func(*testing.T, *store.Store, string)
	}{
		{
			name: "manual credential update",
			change: func(t *testing.T, s *store.Store, id string) {
				if _, err := s.UpdateConnection(store.UpdateConnectionInput{ID: id, Secret: "manual-credential"}); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *store.Store, id string) {
				credential, ok := s.ConnectionCredentialByID(id)
				if !ok || credential.Secret != "manual-credential" {
					t.Fatalf("manual credential was replaced: %#v, exists=%v", credential, ok)
				}
			},
		},
		{
			name: "credential type change",
			change: func(t *testing.T, s *store.Store, id string) {
				if _, err := s.UpdateConnection(store.UpdateConnectionInput{ID: id, CredentialType: "api_key", Secret: "new-api-key"}); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *store.Store, id string) {
				credential, ok := s.ConnectionCredentialByID(id)
				if !ok || credential.Type != "api_key" || credential.Secret != "new-api-key" {
					t.Fatalf("changed credential type or source credential was overwritten: %#v, exists=%v", credential, ok)
				}
			},
		},
		{
			name: "connection disabled",
			change: func(t *testing.T, s *store.Store, id string) {
				disabled := false
				if _, err := s.UpdateConnection(store.UpdateConnectionInput{ID: id, Enabled: &disabled}); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *store.Store, id string) {
				connections, err := s.Connections("")
				if err != nil {
					t.Fatal(err)
				}
				var secret string
				if err := s.DB.QueryRow(`SELECT secret_ref FROM connections WHERE id=?`, id).Scan(&secret); err != nil {
					t.Fatal(err)
				}
				if secret != "original-secret" {
					t.Fatal("disabled connection credential was overwritten")
				}
				for _, connection := range connections {
					if connection.ID == id && connection.Enabled {
						t.Fatal("connection unexpectedly enabled after control update")
					}
				}
			},
		},
		{
			name: "connection removed",
			change: func(t *testing.T, s *store.Store, id string) {
				if err := s.DeleteConnection(id); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *store.Store, id string) {
				if _, ok := s.ConnectionCredentialByID(id); ok {
					t.Fatal("removed connection reappeared after delayed device token")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(t.TempDir() + "/state.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			node, err := s.CreateProviderNode(store.CreateProviderNodeInput{
				Name: "OAuth", Prefix: "oauth", BaseURL: "https://provider.test/v1",
				Protocol: "openai_chat", DefinitionID: "oauth", AuthMode: "oauth2",
			})
			if err != nil {
				t.Fatal(err)
			}
			connection, err := s.CreateConnection(store.CreateConnectionInput{
				ProviderNodeID: node.ID, Name: "account", CredentialType: "oauth2", Secret: "original-secret",
			})
			if err != nil {
				t.Fatal(err)
			}
			flow := &generationDeviceFlow{entered: make(chan struct{}), release: make(chan struct{})}
			d := &Daemon{
				store: s, server: api.NewServer(s),
				providerAuthFlowIDs: map[extensions.Ref]map[string]string{
					provider.ProviderDefinitionRef("oauth", 1): {"oauth2": flow.ID()},
				},
				authFlows: map[string]provider.AuthFlow{flow.ID(): flow},
			}
			t.Cleanup(d.cancelAuthorizationSessions)

			challenge, err := d.startDeviceAuthorization(context.Background(), connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			awaitAuthorizationSignal(t, flow.entered)
			tc.change(t, s, connection.ID)
			close(flow.release)

			drained := make(chan struct{})
			go func() { d.authWG.Wait(); close(drained) }()
			awaitAuthorizationSignal(t, drained)
			status, err := d.deviceAuthorizationStatus(challenge.SessionID)
			if err != nil || status.Status != "failed" {
				t.Fatalf("superseded device authorization status=%+v err=%v", status, err)
			}
			tc.check(t, s, connection.ID)
		})
	}
}
