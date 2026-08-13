package onvif

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/IOTechSystems/onvif"
	wsdiscovery "github.com/IOTechSystems/onvif/ws-discovery"
	"github.com/google/uuid"
	"golang.org/x/net/ipv4"

	"aiovms/pkg/logger"
)

// wsDiscoveryGroup is the ONVIF WS-Discovery multicast address and port.
const (
	wsDiscoveryMulticastAddr = "239.255.255.250:3702"
	defaultMulticastTimeout  = 5 * time.Second
	multicastBufSize         = 8192
)

// multicastProbe sends a WS-Discovery Probe via UDP multicast and collects
// ProbeMatch responses, then converts them to ONVIF devices via the upstream
// parser (DevicesFromProbeResponses).
//
// This replaces the upstream wsdiscovery.GetAvailableDevicesAtSpecificEthernetInterface
// because that function hardcodes a 1-second read deadline — far too short for
// real cameras which typically respond within 2–5 seconds. It also silently
// binds to the system default interface when given an empty name, which on a
// multi-NIC host (physical NIC + Docker veth + virtual adapters) frequently
// sends the multicast on the WRONG interface, so the probe never reaches the
// camera's subnet.
//
// timeoutSec: max wait time for responses (default 5s if <= 0).
// ifaceName: optional network interface to bind (empty = auto-detect the
// interface with a default route to the multicast group, which is more robust
// than the upstream nil-interface JoinGroup).
func multicastProbe(ifaceName string, timeout time.Duration) ([]onvif.Device, error) {
	if timeout <= 0 {
		timeout = defaultMulticastTimeout
	}

	// Build the Probe SOAP message via the upstream builder (reuse, don't
	// reinvent the SOAP envelope).
	probeMsg := wsdiscovery.BuildProbeMessage(
		uuid.NewString(),
		nil,
		[]string{"dn:NetworkVideoTransmitter"},
		map[string]string{"dn": "http://www.onvif.org/ver10/network/wsdl"},
	)

	responses, err := sendUDPMulticast(probeMsg.String(), ifaceName, timeout)
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, nil
	}

	devices, err := wsdiscovery.DevicesFromProbeResponses(responses)
	if err != nil {
		return nil, fmt.Errorf("parse probe responses: %w", err)
	}
	return devices, nil
}

// sendUDPMulticast sends a SOAP probe to the WS-Discovery multicast group and
// reads responses until the timeout expires. Unlike the upstream implementation,
// the read deadline is configurable and the interface binding is explicit.
func sendUDPMulticast(msg string, ifaceName string, timeout time.Duration) ([]string, error) {
	conn, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("listen udp4: %w", err)
	}
	defer conn.Close()

	p := ipv4.NewPacketConn(conn)

	group := net.IPv4(239, 255, 255, 250)
	dest := &net.UDPAddr{IP: group, Port: 3702}

	// Resolve the interface to bind. When empty, auto-select the interface
	// that has a route to the multicast group; otherwise bind explicitly.
	var iface *net.Interface
	if ifaceName == "" {
		iface, err = selectMulticastInterface(group)
		if err != nil {
			logger.Warnf("onvif multicast: auto-select interface failed (%v), falling back to default", err)
			iface = nil
		}
	} else {
		iface, err = net.InterfaceByName(ifaceName)
		if err != nil {
			return nil, fmt.Errorf("interface %q not found: %w", ifaceName, err)
		}
	}

	if err = p.JoinGroup(iface, &net.UDPAddr{IP: group}); err != nil {
		return nil, fmt.Errorf("join multicast group: %w", err)
	}
	if iface != nil {
		if err = p.SetMulticastInterface(iface); err != nil {
			return nil, fmt.Errorf("set multicast interface: %w", err)
		}
		if err = p.SetMulticastTTL(2); err != nil {
			return nil, fmt.Errorf("set multicast ttl: %w", err)
		}
	}

	if _, err = p.WriteTo([]byte(msg), nil, dest); err != nil {
		return nil, fmt.Errorf("write multicast probe: %w", err)
	}

	// Configurable read deadline — the key fix over the upstream 1s hardcode.
	if err = p.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}

	var responses []string
	buf := make([]byte, multicastBufSize)
	for {
		n, _, _, err := p.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			}
			return nil, fmt.Errorf("read multicast response: %w", err)
		}
		responses = append(responses, string(buf[:n]))
	}
	return responses, nil
}

// selectMulticastInterface finds a network interface that has a route to the
// given multicast group. It prefers non-loopback, up, multicast-capable IPv4
// interfaces. Returns nil if none found.
func selectMulticastInterface(group net.IP) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagUp == 0 {
			continue
		}
		if ifaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		if ifaces[i].Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			if ip.To4() != nil {
				logger.Infof("onvif multicast: selected interface %s (%s)", ifaces[i].Name, ip.String())
				return &ifaces[i], nil
			}
		}
	}
	return nil, fmt.Errorf("no multicast-capable IPv4 interface found")
}
