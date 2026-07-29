package traefik_wol

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testClient() *http.Client {
	return &http.Client{Timeout: time.Second}
}

func TestNewHealthCheckerSchemes(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantNetwork string
		wantAddr    string
		wantErr     bool
	}{
		{name: "http", raw: "http://192.168.0.10:8009", wantNetwork: "", wantAddr: ""},
		{name: "https", raw: "https://example.com", wantNetwork: "", wantAddr: ""},
		{name: "tcp", raw: "tcp://192.168.0.10:22", wantNetwork: "tcp", wantAddr: "192.168.0.10:22"},
		{name: "tcp4", raw: "tcp4://192.168.0.10:22", wantNetwork: "tcp4", wantAddr: "192.168.0.10:22"},
		{name: "tcp6", raw: "tcp6://[::1]:22", wantNetwork: "tcp6", wantAddr: "[::1]:22"},
		{name: "tcp without port", raw: "tcp://192.168.0.10", wantErr: true},
		{name: "tcp with path only", raw: "tcp://", wantErr: true},
		{name: "no scheme", raw: "192.168.0.10:8009", wantErr: true},
		{name: "bare host", raw: "192.168.0.10", wantErr: true},
		{name: "unsupported scheme", raw: "udp://192.168.0.10:9", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checker, err := newHealthChecker(test.raw, testClient(), time.Second)
			if test.wantErr {
				if err == nil {
					t.Fatalf("newHealthChecker(%q) expected an error, got none", test.raw)
				}
				return
			}

			if err != nil {
				t.Fatalf("newHealthChecker(%q) returned an unexpected error: %s", test.raw, err)
			}
			if checker.network != test.wantNetwork {
				t.Errorf("network = %q, want %q", checker.network, test.wantNetwork)
			}
			if checker.addr != test.wantAddr {
				t.Errorf("addr = %q, want %q", checker.addr, test.wantAddr)
			}
		})
	}
}

func TestHealthCheckerTCPAlive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	checker, err := newHealthChecker("tcp://"+listener.Addr().String(), testClient(), time.Second)
	if err != nil {
		t.Fatalf("newHealthChecker returned an unexpected error: %s", err)
	}

	if !checker.alive() {
		t.Error("alive() = false for a listening socket, want true")
	}
}

func TestHealthCheckerTCPDown(t *testing.T) {
	// Bind and immediately release the port so that nothing is listening on it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	checker, err := newHealthChecker("tcp://"+addr, testClient(), time.Second)
	if err != nil {
		t.Fatalf("newHealthChecker returned an unexpected error: %s", err)
	}

	if checker.alive() {
		t.Error("alive() = true for a closed port, want false")
	}
}

// TestHealthCheckerTCPIgnoresPayload verifies that a layer 4 check succeeds
// against a service that never speaks HTTP.
func TestHealthCheckerTCPIgnoresPayload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
	}()

	checker, err := newHealthChecker("tcp://"+listener.Addr().String(), testClient(), time.Second)
	if err != nil {
		t.Fatalf("newHealthChecker returned an unexpected error: %s", err)
	}

	if !checker.alive() {
		t.Error("alive() = false for a non-HTTP service, want true")
	}
}

func TestHealthCheckerHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker, err := newHealthChecker(server.URL, testClient(), time.Second)
	if err != nil {
		t.Fatalf("newHealthChecker returned an unexpected error: %s", err)
	}

	if !checker.alive() {
		t.Error("alive() = false for a running HTTP server, want true")
	}

	server.Close()

	if checker.alive() {
		t.Error("alive() = true for a stopped HTTP server, want false")
	}
}

func TestNewRejectsInvalidHealthCheck(t *testing.T) {
	config := CreateConfig()
	config.MacAddress = "00:00:00:00:00:00"
	config.HealthCheck = "udp://192.168.0.10:9"

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	if _, err := New(nil, next, config, "wol"); err == nil {
		t.Error("New() accepted an unsupported healthCheck scheme, want an error")
	}
}

func TestNewAcceptsTCPHealthCheck(t *testing.T) {
	config := CreateConfig()
	config.MacAddress = "00:00:00:00:00:00"
	config.HealthCheck = "tcp://192.168.0.10:22"

	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

	handler, err := New(nil, next, config, "wol")
	if err != nil {
		t.Fatalf("New() returned an unexpected error: %s", err)
	}

	wol, ok := handler.(*Wol)
	if !ok {
		t.Fatalf("New() returned %T, want *Wol", handler)
	}
	if wol.healthCheck.network != "tcp" {
		t.Errorf("network = %q, want \"tcp\"", wol.healthCheck.network)
	}
}

// TestServeHTTPPassesThroughWhenAlive ensures a request is forwarded without
// any wake attempt when the layer 4 check succeeds.
func TestServeHTTPPassesThroughWhenAlive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %s", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	config := CreateConfig()
	config.MacAddress = "00:00:00:00:00:00"
	config.HealthCheck = "tcp://" + listener.Addr().String()

	called := false
	next := http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		called = true
		rw.WriteHeader(http.StatusOK)
	})

	handler, err := New(nil, next, config, "wol")
	if err != nil {
		t.Fatalf("New() returned an unexpected error: %s", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://localhost/", nil))

	if !called {
		t.Error("next handler was not called")
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}
