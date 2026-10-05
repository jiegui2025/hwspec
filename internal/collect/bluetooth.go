package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jiegui2025/hwspec/internal/report"
)

func (c *collector) bluetooth() {
	c.r.Bluetooth = []report.BluetoothController{}
	for _, n := range list("/sys/class/bluetooth") {
		// hci0 is a controller; hci0:256 entries are connections. The
		// management API addresses controllers by a 16-bit index.
		idx, err := strconv.ParseUint(strings.TrimPrefix(n, "hci"), 10, 16)
		if !strings.HasPrefix(n, "hci") || err != nil {
			continue
		}
		bt := report.BluetoothController{Name: n}
		bt.Bus, bt.BusAddress = busOf("/sys/class/bluetooth/" + n + "/device")
		bt.Driver = driverAt("/sys/class/bluetooth/" + n + "/device")
		info, err := mgmtReadInfo(uint16(idx))
		if err != nil {
			c.warn("bluetooth %s: %v", n, err)
		} else {
			bt.Address = info.address
			bt.ManufacturerID = int(info.manufacturer)
			bt.Version = btVersion(info.version)
			bt.LocalName = info.name
			powered := info.settings&1 != 0
			bt.Powered = &powered
		}
		c.r.Bluetooth = append(c.r.Bluetooth, bt)
	}
}

type mgmtInfo struct {
	address      string
	version      byte
	manufacturer uint16
	settings     uint32
	name         string
}

// mgmtReadInfo asks the kernel's Bluetooth management interface (the one
// bluetoothd and btmgmt use) for a controller's details. Read-only commands
// are allowed on unprivileged sockets, so this works without root and
// without bluetoothd running.
func mgmtReadInfo(index uint16) (*mgmtInfo, error) {
	const (
		btprotoHCI         = 1
		hciChannelControl  = 3
		hciDevNone         = 0xFFFF
		opReadInfo         = 0x0004
		evCmdComplete      = 0x0001
		evCmdStatus        = 0x0002
		readInfoReplyBytes = 280
	)
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, btprotoHCI)
	if err != nil {
		return nil, fmt.Errorf("management socket: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: hciDevNone, Channel: hciChannelControl}); err != nil {
		return nil, fmt.Errorf("management socket: %w", err)
	}
	tv := unix.NsecToTimeval((2 * time.Second).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return nil, err
	}

	cmd := make([]byte, 6) // opcode, controller index, parameter length
	binary.LittleEndian.PutUint16(cmd[0:], opReadInfo)
	binary.LittleEndian.PutUint16(cmd[2:], index)
	if _, err := unix.Write(fd, cmd); err != nil {
		return nil, fmt.Errorf("management command: %w", err)
	}

	buf := make([]byte, 1024)
	for tries := 0; tries < 16; tries++ {
		n, err := unix.Read(fd, buf)
		if err != nil {
			return nil, fmt.Errorf("management reply: %w", err)
		}
		if n < 9 {
			continue
		}
		event := binary.LittleEndian.Uint16(buf[0:])
		evIndex := binary.LittleEndian.Uint16(buf[2:])
		opcode := binary.LittleEndian.Uint16(buf[6:])
		status := buf[8]
		if evIndex != index || opcode != opReadInfo || (event != evCmdComplete && event != evCmdStatus) {
			continue // an unrelated event
		}
		if status != 0 {
			return nil, fmt.Errorf("management status %d", status)
		}
		p := buf[9:n]
		if len(p) < readInfoReplyBytes {
			return nil, errors.New("short management reply")
		}
		addr := make([]string, 6)
		for i := 0; i < 6; i++ {
			addr[5-i] = fmt.Sprintf("%02X", p[i]) // little-endian on the wire
		}
		name := p[20 : 20+249]
		if i := strings.IndexByte(string(name), 0); i >= 0 {
			name = name[:i]
		}
		return &mgmtInfo{
			address:      strings.Join(addr, ":"),
			version:      p[6],
			manufacturer: binary.LittleEndian.Uint16(p[7:]),
			settings:     binary.LittleEndian.Uint32(p[13:]),
			name:         string(name),
		}, nil
	}
	return nil, errors.New("no management reply")
}

// btVersion maps the HCI version number to the core specification version.
func btVersion(v byte) string {
	versions := []string{"1.0b", "1.1", "1.2", "2.0", "2.1", "3.0", "4.0", "4.1", "4.2", "5.0", "5.1", "5.2", "5.3", "5.4", "6.0", "6.1"}
	if int(v) < len(versions) {
		return versions[v]
	}
	return fmt.Sprintf("unknown (%d)", v)
}
