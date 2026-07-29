package traefik_wol

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/MarkusJx/traefik-wol/wol"
)

// Config the plugin configuration.
type Config struct {
	MacAddress         string `json:"macAddress,omitempty"`
	IpAddress          string `json:"ipAddress,omitempty"`
	StartUrl           string `json:"startUrl,omitempty"`
	StartMethod        string `json:"startMethod,omitempty"`
	StopUrl            string `json:"stopUrl,omitempty"`
	StopMethod         string `json:"stopMethod,omitempty"`
	StopTimeout        int    `json:"stopTimeout,omitempty"`
	HealthCheck        string `json:"healthCheck,omitempty"`
	BroadcastInterface string `json:"broadcastInterface,omitempty"`
	RequestTimeout     int    `json:"requestTimeout,omitempty"`
	NumRetries         int    `json:"numRetries,omitempty"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		MacAddress:         "",
		IpAddress:          "",
		HealthCheck:        "",
		StartUrl:           "",
		StartMethod:        "GET",
		StopUrl:            "",
		StopMethod:         "GET",
		BroadcastInterface: "",
		StopTimeout:        5,
		RequestTimeout:     5,
		NumRetries:         10,
	}
}

// healthChecker probes the target service to determine whether it is up.
// It supports layer 7 (HTTP/HTTPS) and layer 4 (TCP) checks, selected by the
// scheme of the configured healthCheck value.
type healthChecker struct {
	// raw is the health check as configured, kept for log output.
	raw string
	// network is the dial network for layer 4 checks ("tcp", "tcp4" or
	// "tcp6"). It is empty for HTTP checks.
	network string
	// addr is the "host:port" to dial for layer 4 checks.
	addr    string
	client  *http.Client
	timeout time.Duration
}

// newHealthChecker builds a checker from the configured healthCheck value.
// A http:// or https:// URL performs a layer 7 check, a tcp://, tcp4:// or
// tcp6:// URL performs a layer 4 check which only opens a connection to the
// given host and port.
func newHealthChecker(raw string, client *http.Client, timeout time.Duration) (*healthChecker, error) {
	if !strings.Contains(raw, "://") {
		return nil, fmt.Errorf(
			"healthCheck %q must include a scheme, e.g. http://host:port or tcp://host:port", raw)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("healthCheck %q is not a valid URL: %w", raw, err)
	}

	switch parsed.Scheme {
	case "http", "https":
		return &healthChecker{raw: raw, client: client, timeout: timeout}, nil
	case "tcp", "tcp4", "tcp6":
		if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
			return nil, fmt.Errorf(
				"healthCheck %q must contain a host and a port, e.g. tcp://host:port", raw)
		}

		return &healthChecker{
			raw:     raw,
			network: parsed.Scheme,
			addr:    parsed.Host,
			client:  client,
			timeout: timeout,
		}, nil
	default:
		return nil, fmt.Errorf(
			"unsupported healthCheck scheme %q, expected one of http, https, tcp, tcp4 or tcp6", parsed.Scheme)
	}
}

// alive reports whether the target service currently accepts connections.
func (h *healthChecker) alive() bool {
	if len(h.network) == 0 {
		_, err := h.client.Get(h.raw)
		if err != nil {
			fmt.Printf("Server is down: %s\n", err)
			return false
		}

		return true
	}

	conn, err := net.DialTimeout(h.network, h.addr, h.timeout)
	if err != nil {
		fmt.Printf("Server is down: %s\n", err)
		return false
	}
	defer conn.Close()

	return true
}

// Wol a Demo plugin.
type Wol struct {
	next               http.Handler
	macAddress         string
	ipAddress          string
	startUrl           string
	startMethod        string
	stopUrl            string
	healthCheck        *healthChecker
	name               string
	broadcastInterface string
	stopMethod         string
	stopTimeout        int
	numRetries         int
	client             *http.Client
	sleepTimer         *time.Timer
	timerMutex         *sync.Mutex
	template           *template.Template
}

// New created a new Demo plugin.
func New(_ context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if len(config.HealthCheck) == 0 {
		return nil, fmt.Errorf("healthCheck cannot be empty")
	}

	if len(config.MacAddress) == 0 && len(config.StartUrl) == 0 {
		return nil, fmt.Errorf("either mac or startUrl must be set")
	}

	if len(config.StartUrl) > 0 && len(config.MacAddress) > 0 {
		return nil, fmt.Errorf("cannot use mac and startUrl at the same time")
	}

	if config.StopTimeout < 1 {
		return nil, fmt.Errorf("stopTimeout must be at least 1")
	}

	if len(config.StopMethod) > 0 && config.StopMethod != "GET" && config.StopMethod != "POST" {
		return nil, fmt.Errorf("stopMethod must be either GET or POST")
	}

	if len(config.StartMethod) > 0 && config.StartMethod != "GET" && config.StartMethod != "POST" {
		return nil, fmt.Errorf("startMethod must be either GET or POST")
	}

	if config.RequestTimeout < 1 {
		return nil, fmt.Errorf("requestTimeout must be at least 1")
	}

	if config.NumRetries < 1 {
		return nil, fmt.Errorf("numRetries must be at least 1")
	}

	requestTimeout := time.Duration(config.RequestTimeout) * time.Second
	client := &http.Client{
		Timeout: requestTimeout,
	}

	healthCheck, err := newHealthChecker(config.HealthCheck, client, requestTimeout)
	if err != nil {
		return nil, err
	}

	var sleepTimer *time.Timer
	if len(config.StopUrl) > 0 {
		fmt.Println("Starting sleep timer")
		sleepTimer = time.AfterFunc(time.Duration(config.StopTimeout)*time.Minute, func() {
			if !healthCheck.alive() {
				fmt.Println("Server is already stopped")
				return
			}

			var err error
			fmt.Printf("Attempting to stop server at %s\n", config.StopUrl)
			switch config.StopMethod {
			case "GET":
				_, err = client.Get(config.StopUrl)
			case "POST":
				_, err = client.Post(config.StopUrl, "application/json", nil)
			default:
				err = fmt.Errorf("unknown stop method: %s", config.StopMethod)
			}

			if err != nil {
				fmt.Printf("Error while stopping server: %s\n", err)
			}
		})
	}

	return &Wol{
		healthCheck:        healthCheck,
		macAddress:         config.MacAddress,
		ipAddress:          config.IpAddress,
		startUrl:           config.StartUrl,
		startMethod:        config.StartMethod,
		stopUrl:            config.StopUrl,
		next:               next,
		name:               name,
		broadcastInterface: config.BroadcastInterface,
		stopMethod:         config.StopMethod,
		stopTimeout:        config.StopTimeout,
		sleepTimer:         sleepTimer,
		client:             client,
		numRetries:         config.NumRetries,
		timerMutex:         &sync.Mutex{},
		template:           template.New("wol").Delims("[[", "]]"),
	}, nil
}

func (a *Wol) resetTimer() {
	a.timerMutex.Lock()
	if a.sleepTimer != nil {
		fmt.Println("Resetting sleep timer")
		a.sleepTimer.Reset(time.Duration(a.stopTimeout) * time.Minute)
	}
	a.timerMutex.Unlock()
}

func ipFromInterface(iface string) (*net.UDPAddr, error) {
	ief, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}

	addrs, err := ief.Addrs()
	if err == nil && len(addrs) <= 0 {
		err = fmt.Errorf("no address associated with interface %s", iface)
	}
	if err != nil {
		return nil, err
	}

	// Validate that one of the addrs is a valid network IP address.
	for _, addr := range addrs {
		switch ip := addr.(type) {
		case *net.IPNet:
			if !ip.IP.IsLoopback() && ip.IP.To4() != nil {
				return &net.UDPAddr{
					IP: ip.IP,
				}, nil
			}
		}
	}
	return nil, fmt.Errorf("no address associated with interface %s", iface)
}

func (a *Wol) wakeUp() error {
	if len(a.startUrl) > 0 {
		fmt.Printf("Attempting to start server at %s %s\n", a.startMethod, a.startUrl)
		var err error
		switch a.startMethod {
		case "GET":
			_, err = http.Get(a.startUrl)
		case "POST":
			_, err = http.Post(a.startUrl, "text/plain", nil)
		default:
			err = fmt.Errorf("unknown start method: %s", a.startMethod)
		}

		if err != nil {
			return err
		}

		return nil
	}

	var localAddr *net.UDPAddr
	var err error
	if len(a.broadcastInterface) > 0 {
		localAddr, err = ipFromInterface(a.broadcastInterface)
		if err != nil {
			return err
		}
	}

	targetIP := "255.255.255.255"
	if len(a.ipAddress) > 0 {
		targetIP = a.ipAddress
	}

	bcastAddr := fmt.Sprintf("%s:%s", targetIP, "9")
	udpAddr, err := net.ResolveUDPAddr("udp", bcastAddr)
	if err != nil {
		return err
	}

	mp, err := wol.New(a.macAddress)
	if err != nil {
		return err
	}

	bs, err := mp.Marshal()
	if err != nil {
		return err
	}

	conn, err := net.DialUDP("udp", localAddr, udpAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	fmt.Printf("Attempting to send a magic packet to MAC %s\n", a.macAddress)
	fmt.Printf("... Broadcasting to: %s\n", bcastAddr)
	n, err := conn.Write(bs)
	if err == nil && n != 102 {
		err = fmt.Errorf("magic packet sent was %d bytes (expected 102 bytes sent)", n)
	}
	if err != nil {
		return err
	}

	return nil
}

func (a *Wol) serviceIsAlive() bool {
	fmt.Printf("Checking if server is up at %s\n", a.healthCheck.raw)
	if !a.healthCheck.alive() {
		return false
	}

	fmt.Println("Server is up")
	return true
}

func (a *Wol) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	a.resetTimer()
	if !a.serviceIsAlive() {
		fmt.Println("Server is down, waking up")
		err := a.wakeUp()
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Println("Waiting for server to come up")
		for i := 0; i < a.numRetries; i++ {
			if a.serviceIsAlive() {
				fmt.Println("Server is up")
				break
			}

			time.Sleep(5 * time.Second)
		}

		if !a.serviceIsAlive() {
			http.Error(rw, "Failed to start server", http.StatusInternalServerError)
			return
		}
	}

	a.next.ServeHTTP(rw, req)
}
