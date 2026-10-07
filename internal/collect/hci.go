package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// A Bluetooth controller's firmware build, from HCI Read Local Version
// Information (#202): its HCI revision and LMP subversion. The kernel's
// management interface doesn't give them, so hwspec sends the HCI command
// on a raw socket, which needs no privilege for this command: the kernel's
// security filter lets unprivileged sockets send OGF 0x04, OCF 0x0001
// (net/bluetooth/hci_sock.c, hci_sec_filter). Layouts are BlueZ's
// lib/bluetooth/hci.h; numbers are little-endian.

const (
	hciCommandPkt     = 0x01   // HCI_COMMAND_PKT
	hciEventPkt       = 0x04   // HCI_EVENT_PKT
	evtCmdComplete    = 0x0E   // EVT_CMD_COMPLETE: ncmd, opcode, return parameters
	evtCmdStatus      = 0x0F   // EVT_CMD_STATUS: status, ncmd, opcode
	opReadLocalVer    = 0x1001 // cmd_opcode_pack(OGF_INFO_PARAM 0x04, OCF_READ_LOCAL_VERSION 0x0001)
	localVersionBytes = 9      // READ_LOCAL_VERSION_RP_SIZE
)

// hciVersion is read_local_version_rp without its status.
type hciVersion struct {
	hciVersion   byte
	hciRevision  uint16
	lmpVersion   byte
	manufacturer uint16
	lmpSubver    uint16
}

// readBTVersion is a seam: the real socket needs a Bluetooth controller.
var readBTVersion = hciReadLocalVersion

// openHCI opens a raw HCI socket on one controller that passes only
// command complete and status events; tests replace it.
var openHCI = func(dev uint16) (mgmtConn, error) {
	const (
		btprotoHCI    = 1 // BTPROTO_HCI
		solHCI        = 0 // SOL_HCI
		hciFilter     = 2 // HCI_FILTER
		hciChannelRaw = 0 // HCI_CHANNEL_RAW
	)
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, btprotoHCI)
	if err != nil {
		return nil, fmt.Errorf("HCI socket: %w", err)
	}
	// struct hci_filter: type_mask, event_mask[2], opcode, padded to 16
	// bytes as the kernel's struct hci_ufilter is; host byte order.
	filter := make([]byte, 16)
	binary.NativeEndian.PutUint32(filter[0:], 1<<hciEventPkt)
	binary.NativeEndian.PutUint32(filter[4:], 1<<evtCmdComplete|1<<evtCmdStatus)
	tv := unix.NsecToTimeval((2 * time.Second).Nanoseconds())
	for _, step := range []func() error{
		func() error { return unix.Bind(fd, &unix.SockaddrHCI{Dev: dev, Channel: hciChannelRaw}) },
		func() error { return unix.SetsockoptString(fd, solHCI, hciFilter, string(filter)) },
		func() error { return unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv) },
	} {
		if err := step(); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("HCI socket: %w", err)
		}
	}
	return fdConn(fd), nil
}

// hciReadLocalVersion asks a controller for its local version information.
func hciReadLocalVersion(index uint16) (*hciVersion, error) {
	conn, err := openHCI(index)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	cmd := []byte{hciCommandPkt, 0, 0, 0} // packet type, opcode, parameter length
	binary.LittleEndian.PutUint16(cmd[1:], opReadLocalVer)
	if _, err := conn.Write(cmd); err != nil {
		return nil, fmt.Errorf("HCI command: %w", err)
	}
	// Other commands' events may arrive too; only this one's counts.
	buf := make([]byte, 260) // packet type, event header, at most 255 parameter bytes
	for range 16 {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("HCI reply: %w", err)
		}
		if v, err := parseLocalVersion(buf[:n]); v != nil || err != nil {
			return v, err
		}
	}
	return nil, errors.New("no HCI reply")
}

// parseLocalVersion decodes one HCI event packet. It returns (nil, nil)
// for an event that isn't this command's reply, so the caller keeps
// reading.
func parseLocalVersion(pkt []byte) (*hciVersion, error) {
	if len(pkt) < 3 || pkt[0] != hciEventPkt || int(pkt[2]) != len(pkt)-3 {
		return nil, nil
	}
	p := pkt[3:]
	switch pkt[1] {
	case evtCmdStatus: // the command was refused before it ran
		if len(p) < 4 || binary.LittleEndian.Uint16(p[2:]) != opReadLocalVer {
			return nil, nil
		}
		if p[0] != 0 {
			return nil, fmt.Errorf("HCI command status 0x%02x", p[0])
		}
		return nil, nil
	case evtCmdComplete:
		if len(p) < 3 || binary.LittleEndian.Uint16(p[1:]) != opReadLocalVer {
			return nil, nil
		}
		r := p[3:]
		if len(r) < 1 {
			return nil, errors.New("HCI reply without a status")
		}
		if r[0] != 0 {
			return nil, fmt.Errorf("HCI status 0x%02x", r[0])
		}
		if len(r) < localVersionBytes {
			return nil, fmt.Errorf("HCI reply of %d bytes, want %d", len(r), localVersionBytes)
		}
		return &hciVersion{
			hciVersion:   r[1],
			hciRevision:  binary.LittleEndian.Uint16(r[2:]),
			lmpVersion:   r[4],
			manufacturer: binary.LittleEndian.Uint16(r[5:]),
			lmpSubver:    binary.LittleEndian.Uint16(r[7:]),
		}, nil
	}
	return nil, nil
}
