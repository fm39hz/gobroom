package daemon

import "testing"

func TestCallbackIngressConfigValidation(t *testing.T) {
	if got, err := callbackIngressConfig(AuthorizationCallbackIngressConfig{}); got != nil || err != nil {
		t.Fatalf("zero config should be disabled without error: %v %v", got, err)
	}
	tests := []struct {
		name   string
		config AuthorizationCallbackIngressConfig
		want   bool
	}{
		{"valid", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com/", ListenAddress: "127.0.0.1:0"}, true},
		{"missing address", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com"}, false},
		{"missing origin", AuthorizationCallbackIngressConfig{ListenAddress: "127.0.0.1:8080"}, false},
		{"http origin", AuthorizationCallbackIngressConfig{PublicOrigin: "http://auth.example.com", ListenAddress: "127.0.0.1:8080"}, false},
		{"credentials", AuthorizationCallbackIngressConfig{PublicOrigin: "https://user@auth.example.com", ListenAddress: "127.0.0.1:8080"}, false},
		{"query", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com?x=1", ListenAddress: "127.0.0.1:8080"}, false},
		{"path", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com/callback", ListenAddress: "127.0.0.1:8080"}, false},
		{"zero public port", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com:0", ListenAddress: "127.0.0.1:8080"}, false},
		{"DNS bind", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com", ListenAddress: "localhost:8080"}, false},
		{"nonloopback bind", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com", ListenAddress: "0.0.0.0:8080"}, false},
		{"zone bind", AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com", ListenAddress: "[::1%lo]:8080"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := callbackIngressConfig(test.config)
			if (err == nil) != test.want || (got != nil) != test.want {
				t.Fatalf("callbackIngressConfig() = (%#v, %v), want enabled=%v", got, err, test.want)
			}
		})
	}
}
