package inbox

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"
)

func ValidateURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(allowPrivate && u.Scheme == "http")) {
		return errors.New("destination requires HTTPS (HTTP only for explicitly allowed private endpoints)")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !allowPrivate && !publicIP(ip) {
		return errors.New("private destination is not allowed")
	}
	return nil
}

func publicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// 在实际连接时解析并固定地址，阻止重定向和 DNS 重绑定访问私网。
func SafeClient(allowPrivate bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConns: 8, IdleConnTimeout: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errors.New("destination DNS lookup failed")
		}
		if len(ips) == 0 {
			return nil, errors.New("destination has no address")
		}
		for _, ip := range ips {
			if !allowPrivate && !publicIP(ip.IP) {
				return nil, errors.New("private destination is not allowed")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("redirects are not allowed") }}
}
