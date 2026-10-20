package daemon

import (
	"context"
	"net"
	"os"
	"testing"
)

func TestCallbackIngressBindFailureRollsBackHTTPReservations(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	config := listenerTestConfig(t)
	dataAddress := unusedListenerAddress(t)
	config.HTTPAddr = dataAddress
	config.CallbackIngress = AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com", ListenAddress: occupied.Addr().String()}
	d := New(config)
	if err := d.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded with an occupied callback ingress address")
	}
	rebound, err := net.Listen("tcp", dataAddress)
	if err != nil {
		t.Fatalf("callback bind failure leaked the data listener: %v", err)
	}
	_ = rebound.Close()
	for _, path := range []string{config.DBPath, config.IPCPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("callback bind failure changed %q: %v", path, err)
		}
	}
}

func TestCallbackIngressAddressReportsActualBinding(t *testing.T) {
	config := listenerTestConfig(t)
	config.CallbackIngress = AuthorizationCallbackIngressConfig{PublicOrigin: "https://auth.example.com", ListenAddress: "127.0.0.1:0"}
	d := New(config)
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer d.Stop(context.Background())
	addresses := d.Addresses()
	if addresses.CallbackIngress == "" || addresses.CallbackIngress == "127.0.0.1:0" {
		t.Fatalf("callback ingress address does not report the actual binding: %#v", addresses)
	}
	conn, err := net.Dial("tcp", addresses.CallbackIngress)
	if err != nil {
		t.Fatalf("reported callback ingress is not listening: %v", err)
	}
	_ = conn.Close()
	if d.callbackIngress == nil || d.callbackIngress.address != addresses.CallbackIngress {
		t.Fatal("validated callback ingress was not available to daemon handlers")
	}
}

func TestCallbackIngressIsDisabledByDefault(t *testing.T) {
	config := listenerTestConfig(t)
	config.CallbackIngress = AuthorizationCallbackIngressConfig{}
	d := New(config)
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer d.Stop(context.Background())
	if addresses := d.Addresses(); addresses.CallbackIngress != "" || d.callbackIngress != nil || d.httpCallback != nil {
		t.Fatalf("default configuration opened callback ingress: %#v", addresses)
	}
}
