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
		bt := report.BluetoothController{Name: n, Firmware: c.btFirmware(n, uint16(idx))}
		bt.Bus, bt.BusAddress = busOf("/sys/class/bluetooth/" + n + "/device")
		bt.Driver = c.driverAt("/sys/class/bluetooth/" + n + "/device")
		info, err := readBTInfo(uint16(idx))
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

// btFirmware reads a controller's firmware build from HCI Read Local
// Version Information: the LMP subversion as the version and the HCI
// revision as the release, in hex as the specification writes them (an
// Intel AX200: 0x21c1 both, its build 193 of week 33). A controller that
// is down, or a refused command, gives a warning and the reason.
func (c *collector) btFirmware(name string, index uint16) *report.Firmware {
	v, err := readBTVersion(index)
	if err != nil {
		c.warn("bluetooth %s: firmware version: %v", name, err)
		switch {
		case errors.Is(err, unix.ENETDOWN):
			return report.UnknownFirmware("the controller is down (powered off or blocked)")
		case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
			return report.UnknownFirmware("the kernel refused the HCI command")
		}
		return report.UnknownFirmware("the controller didn't answer HCI Read Local Version")
	}
	return &report.Firmware{Version: fmt.Sprintf("0x%04x", v.lmpSubver), Release: fmt.Sprintf("0x%04x", v.hciRevision), Source: report.FirmwareFromHCI}
}

type mgmtInfo struct {
	address      string
	version      byte
	manufacturer uint16
	settings     uint32
	name         string
}

// readBTInfo is a seam: the real socket needs a Bluetooth controller.
var readBTInfo = mgmtReadInfo

// mgmtConn is a connection to the kernel's Bluetooth management interface.
type mgmtConn interface {
	Write(b []byte) (int, error)
	Read(b []byte) (int, error)
	Close() error
}

// openMgmt opens the management socket; tests replace it with a scripted
// connection.
var openMgmt = func() (mgmtConn, error) {
	const (
		btprotoHCI        = 1
		hciChannelControl = 3
		hciDevNone        = 0xFFFF
	)
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, btprotoHCI)
	if err != nil {
		return nil, fmt.Errorf("management socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: hciDevNone, Channel: hciChannelControl}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("management socket: %w", err)
	}
	tv := unix.NsecToTimeval((2 * time.Second).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("management socket: %w", err)
	}
	return fdConn(fd), nil
}

type fdConn int

func (f fdConn) Write(b []byte) (int, error) { return unix.Write(int(f), b) }
func (f fdConn) Read(b []byte) (int, error)  { return unix.Read(int(f), b) }
func (f fdConn) Close() error                { return unix.Close(int(f)) }

// mgmtReadInfo asks the kernel's Bluetooth management interface (the one
// bluetoothd and btmgmt use) for a controller's details. Read-only commands
// are allowed on unprivileged sockets, so this works without root and
// without bluetoothd running.
func mgmtReadInfo(index uint16) (*mgmtInfo, error) {
	conn, err := openMgmt()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	cmd := make([]byte, 6) // opcode, controller index, parameter length
	binary.LittleEndian.PutUint16(cmd[0:], opReadInfo)
	binary.LittleEndian.PutUint16(cmd[2:], index)
	if _, err := conn.Write(cmd); err != nil {
		return nil, fmt.Errorf("management command: %w", err)
	}

	// Other events (from other controllers, other clients' commands) may
	// arrive first; only our command's reply counts.
	buf := make([]byte, 1024)
	for range 16 {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("management reply: %w", err)
		}
		if info, err := parseReadInfoReply(index, buf[:n]); info != nil || err != nil {
			return info, err
		}
	}
	return nil, errors.New("no management reply")
}

const (
	opReadInfo    = 0x0004
	evCmdComplete = 0x0001
	evCmdStatus   = 0x0002
	// address 6, version 1, manufacturer 2, supported and current settings
	// 4+4, class 3, name 249, short name 11.
	readInfoReplyBytes = 280
)

// parseReadInfoReply decodes one management event. It returns (nil, nil)
// for an unrelated event, so the caller keeps reading.
func parseReadInfoReply(index uint16, ev []byte) (*mgmtInfo, error) {
	if len(ev) < 9 {
		return nil, nil
	}
	event := binary.LittleEndian.Uint16(ev[0:])
	evIndex := binary.LittleEndian.Uint16(ev[2:])
	opcode := binary.LittleEndian.Uint16(ev[6:])
	status := ev[8]
	if evIndex != index || opcode != opReadInfo || (event != evCmdComplete && event != evCmdStatus) {
		return nil, nil
	}
	if status != 0 {
		return nil, fmt.Errorf("management status %d", status)
	}
	p := ev[9:]
	if len(p) < readInfoReplyBytes {
		return nil, errors.New("short management reply")
	}
	addr := make([]string, 6)
	for i := range 6 {
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

// btVersions are the core specification versions by HCI version number.
var btVersions = []string{"1.0b", "1.1", "1.2", "2.0", "2.1", "3.0", "4.0", "4.1", "4.2", "5.0", "5.1", "5.2", "5.3", "5.4", "6.0", "6.1"}

// btVersion maps the HCI version number to the core specification version.
func btVersion(v byte) string {
	if int(v) < len(btVersions) {
		return btVersions[v]
	}
	return fmt.Sprintf("unknown (%d)", v)
}
