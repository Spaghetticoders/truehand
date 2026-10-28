//go:build !darwin && !linux

package serve

import "net"

func peerPID(net.Conn) int { return 0 }
