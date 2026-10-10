package dataplane

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

// ValidateDevelopmentListenAddress permits only literal loopback IPs with a
// numeric TCP port. It is intended for unauthenticated development listeners.
func ValidateDevelopmentListenAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("development listener must be a literal loopback IP and numeric port: %w", err)
	}
	if port == "" {
		return errors.New("development listener port is required")
	}
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return errors.New("development listener port must be numeric")
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("development listener port must be between 0 and 65535")
	}
	ip := net.ParseIP(host)
	if ip == nil || !(ip.To4() != nil && ip.IsLoopback()) && !ip.Equal(net.IPv6loopback) {
		return errors.New("development listener address must be a literal loopback IP")
	}
	return nil
}
