package platform

import "net"

// Adapter is a network adapter with its IPv4 address: Home Assistant needs its MAC address to
// wake the computer.
type Adapter struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	IPv4 string `json:"ipv4"`
}

// Adapters lists the wired and wireless adapters that are up and have an IPv4 address.
func Adapters() []Adapter {
	var out []Adapter
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || len(i.HardwareAddr) != 6 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				out = append(out, Adapter{Name: i.Name, MAC: i.HardwareAddr.String(), IPv4: n.IP.String()})
				break
			}
		}
	}
	return out
}
