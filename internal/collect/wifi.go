package collect

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jiegui2025/hwspec/internal/report"
)

// A Wi-Fi radio's capabilities come from nl80211, the kernel's generic
// netlink interface for wireless (#111): `iw phy` asks the same question.
// sysfs has none of them.

// wifiRadios returns the radios of the wireless NICs (name → wiphy
// index), by name: one nl80211 exchange for all of them, given up on after
// slowAnswer (the wiphy dump takes the RTNL lock, as ethtool does, #243).
// A failure is a warning.
func (c *collector) wifiRadios(ifaces map[string]int) map[string]*report.WiFiRadio {
	if len(ifaces) == 0 {
		return nil
	}
	read := readRadios // read here, not by an exchange left behind
	radios, err := within(slowAnswer, func() (map[string]*report.WiFiRadio, error) { return read(ifaces) })
	if err != nil {
		c.warn("network: Wi-Fi radios (nl80211): %v", err)
		return nil
	}
	return radios
}

// readRadios is a seam: the radios of these interfaces (name → wiphy
// index), by name; one nl80211 doesn't describe is missing, and so is
// every one on a kernel without nl80211. Recordings answer per interface.
var readRadios = nl80211Radios

// errNoNL80211 is a kernel without nl80211 (no cfg80211): no radio, and
// nothing to warn about.
var errNoNL80211 = errors.New("no nl80211")

// nl80211Radios dumps the wiphys once and returns the interfaces' radios.
// A dump the kernel marks inconsistent (a wiphy came or went) is retried
// once.
func nl80211Radios(ifaces map[string]int) (map[string]*report.WiFiRadio, error) {
	conn, err := openGenetlink()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	family, err := genlFamily(conn, "nl80211")
	if errors.Is(err, errNoNL80211) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var wiphys map[int]*wiphy
	for attempt := range 2 {
		msgs, intr, err := genlDump(conn, family, uint32(2+attempt), unix.NL80211_CMD_GET_WIPHY, attr(unix.NL80211_ATTR_SPLIT_WIPHY_DUMP, nil))
		if err != nil {
			return nil, err
		}
		if !intr {
			wiphys = parseWiphys(msgs)
			break
		}
	}
	if wiphys == nil {
		return nil, errors.New("nl80211: the wiphy dump changed while it was read, twice")
	}
	out := map[string]*report.WiFiRadio{}
	for name, index := range ifaces {
		if w := wiphys[index]; w != nil {
			out[name] = w.radio()
		}
	}
	return out, nil
}

// openGenetlink opens a generic netlink socket to the kernel. Requests go
// out with write(2): a netlink socket that isn't connected sends to the
// kernel.
var openGenetlink = func() (mgmtConn, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_GENERIC)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	tv := unix.NsecToTimeval((2 * time.Second).Nanoseconds())
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	return fdConn(fd), nil
}

// attr is a netlink attribute: length, type, payload, padded to 4 bytes.
func attr(typ uint16, payload []byte) []byte {
	b := make([]byte, 4, 4+len(payload)+3)
	binary.LittleEndian.PutUint16(b[0:], uint16(4+len(payload)))
	binary.LittleEndian.PutUint16(b[2:], typ)
	b = append(b, payload...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// genlRequest is a netlink message with a generic netlink header.
func genlRequest(family, flags uint16, seq uint32, cmd uint8, attrs []byte) []byte {
	b := make([]byte, 20, 20+len(attrs))
	binary.LittleEndian.PutUint32(b[0:], uint32(20+len(attrs)))
	binary.LittleEndian.PutUint16(b[4:], family)
	binary.LittleEndian.PutUint16(b[6:], flags)
	binary.LittleEndian.PutUint32(b[8:], seq)
	b[16] = cmd
	b[17] = 1 // version
	return append(b, attrs...)
}

// genlFamily resolves a generic netlink family's ID by name; an unknown
// family (ENOENT) is errNoNL80211.
func genlFamily(conn mgmtConn, name string) (uint16, error) {
	req := genlRequest(unix.GENL_ID_CTRL, unix.NLM_F_REQUEST, 1, unix.CTRL_CMD_GETFAMILY,
		attr(unix.CTRL_ATTR_FAMILY_NAME, append([]byte(name), 0)))
	if _, err := conn.Write(req); err != nil {
		return 0, fmt.Errorf("netlink request: %w", err)
	}
	msgs, _, err := readMessages(conn, 1, false)
	if errors.Is(err, unix.ENOENT) {
		return 0, errNoNL80211
	}
	if err != nil {
		return 0, err
	}
	for _, m := range msgs {
		for _, a := range attrs(m) {
			if a.typ == unix.CTRL_ATTR_FAMILY_ID && len(a.data) >= 2 {
				return binary.LittleEndian.Uint16(a.data), nil
			}
		}
	}
	return 0, errors.New("netlink: no family ID in the reply")
}

// genlDump sends a dump request and returns its messages' payloads (the
// part after the generic netlink header), and whether the kernel marked
// the dump inconsistent.
func genlDump(conn mgmtConn, family uint16, seq uint32, cmd uint8, a []byte) ([][]byte, bool, error) {
	if _, err := conn.Write(genlRequest(family, unix.NLM_F_REQUEST|unix.NLM_F_DUMP, seq, cmd, a)); err != nil {
		return nil, false, fmt.Errorf("netlink request: %w", err)
	}
	return readMessages(conn, seq, true)
}

// readMessages reads the replies to request seq: until NLMSG_DONE for a
// dump, or the first reply otherwise. Each payload is copied out of the
// read buffer, which the next read reuses. An NLMSG_ERROR with a code is
// that errno.
func readMessages(conn mgmtConn, seq uint32, dump bool) (out [][]byte, intr bool, err error) {
	buf := make([]byte, 64*1024)
	for range 4096 {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, false, fmt.Errorf("netlink reply: %w", err)
		}
		done, i, err := splitMessages(buf[:n], seq, &out)
		if err != nil {
			return nil, false, err
		}
		intr = intr || i
		if done || !dump && len(out) > 0 {
			return out, intr, nil
		}
	}
	return nil, false, errors.New("netlink: dump didn't end")
}

// splitMessages appends a copy of each generic netlink message's payload
// in b that answers request seq to out; it says whether b ended the dump,
// and whether a message was marked NLM_F_DUMP_INTR (the dump changed).
func splitMessages(b []byte, seq uint32, out *[][]byte) (done, intr bool, err error) {
	for len(b) >= 16 {
		size := int(binary.LittleEndian.Uint32(b[0:]))
		if size < 16 || size > len(b) {
			return false, false, errors.New("netlink: truncated message")
		}
		typ, flags := binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:])
		m := b[16:size]
		mine := binary.LittleEndian.Uint32(b[8:]) == seq
		b = b[min((size+3)&^3, len(b)):]
		switch {
		case !mine || typ == unix.NLMSG_NOOP:
			continue // another request's reply, or padding
		case typ == unix.NLMSG_OVERRUN:
			return false, false, errors.New("netlink: the kernel's reply overran the socket buffer")
		case typ == unix.NLMSG_DONE:
			return true, intr, nil
		case typ == unix.NLMSG_ERROR:
			if len(m) < 4 {
				return false, false, errors.New("netlink: truncated error")
			}
			if code := int32(binary.LittleEndian.Uint32(m)); code != 0 {
				return false, false, fmt.Errorf("netlink: %w", unix.Errno(-code))
			}
		default:
			intr = intr || flags&unix.NLM_F_DUMP_INTR != 0
			if len(m) >= 4 {
				*out = append(*out, append([]byte(nil), m[4:]...)) // past the generic netlink header
			}
		}
	}
	return false, intr, nil
}

type nlattr struct {
	typ  uint16
	data []byte
}

// attrs splits a run of netlink attributes; a malformed one ends it.
func attrs(b []byte) []nlattr {
	var out []nlattr
	for len(b) >= 4 {
		size := int(binary.LittleEndian.Uint16(b[0:]))
		if size < 4 || size > len(b) {
			break
		}
		out = append(out, nlattr{typ: binary.LittleEndian.Uint16(b[2:]) & 0x3fff, data: b[4:size]})
		b = b[min((size+3)&^3, len(b)):]
	}
	return out
}

// wiphy is what nl80211's split dump says of one radio, merged across its
// messages.
type wiphy struct {
	bands            map[uint16]bool // nl80211 band numbers
	ht, vht, he, eht bool
	tx, rx           uint32 // available antennas
	streams          int
}

// parseWiphys merges a GET_WIPHY split dump's messages by wiphy index.
func parseWiphys(msgs [][]byte) map[int]*wiphy {
	out := map[int]*wiphy{}
	for _, m := range msgs {
		as := attrs(m)
		index := -1
		for _, a := range as {
			if a.typ == unix.NL80211_ATTR_WIPHY && len(a.data) >= 4 {
				index = int(binary.LittleEndian.Uint32(a.data))
			}
		}
		if index < 0 {
			continue
		}
		w := out[index]
		if w == nil {
			w = &wiphy{bands: map[uint16]bool{}}
			out[index] = w
		}
		for _, a := range as {
			switch a.typ {
			case unix.NL80211_ATTR_WIPHY_ANTENNA_AVAIL_TX:
				w.tx = u32(a.data)
			case unix.NL80211_ATTR_WIPHY_ANTENNA_AVAIL_RX:
				w.rx = u32(a.data)
			case unix.NL80211_ATTR_WIPHY_BANDS:
				for _, band := range attrs(a.data) {
					w.bands[band.typ] = true
					w.band(attrs(band.data))
				}
			}
		}
	}
	return out
}

// band reads one band's capabilities and MCS maps.
func (w *wiphy) band(as []nlattr) {
	for _, a := range as {
		switch a.typ {
		case unix.NL80211_BAND_ATTR_HT_CAPA:
			w.ht = true
		case unix.NL80211_BAND_ATTR_HT_MCS_SET:
			// The RX MCS bitmask's first 4 bytes: one byte of 8 MCS per stream.
			n := 0
			for _, b := range a.data[:min(4, len(a.data))] {
				if b != 0 {
					n++
				}
			}
			w.streams = max(w.streams, n)
		case unix.NL80211_BAND_ATTR_VHT_CAPA:
			w.vht = true
		case unix.NL80211_BAND_ATTR_VHT_MCS_SET:
			if len(a.data) >= 2 {
				w.streams = max(w.streams, mcsMapStreams(binary.LittleEndian.Uint16(a.data)))
			}
		case unix.NL80211_BAND_ATTR_IFTYPE_DATA:
			for _, iftype := range attrs(a.data) {
				for _, c := range attrs(iftype.data) {
					switch c.typ {
					case unix.NL80211_BAND_IFTYPE_ATTR_HE_CAP_PHY:
						w.he = true
					case unix.NL80211_BAND_IFTYPE_ATTR_HE_CAP_MCS_SET:
						if len(c.data) >= 2 { // the RX map for up to 80 MHz
							w.streams = max(w.streams, mcsMapStreams(binary.LittleEndian.Uint16(c.data)))
						}
					case unix.NL80211_BAND_IFTYPE_ATTR_EHT_CAP_PHY:
						w.eht = true
					}
				}
			}
		}
	}
}

// mcsMapStreams counts the streams a VHT/HE MCS map supports: 2 bits per
// stream, 3 for "not supported".
func mcsMapStreams(m uint16) int {
	n := 0
	for s := range 8 {
		if m>>(2*s)&3 != 3 {
			n = s + 1
		}
	}
	return n
}

func u32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

// bandNames are nl80211's band numbers in the capture's words.
var bandNames = []struct {
	n    uint16
	name string
}{
	{unix.NL80211_BAND_2GHZ, "2.4 GHz"}, {unix.NL80211_BAND_5GHZ, "5 GHz"},
	{unix.NL80211_BAND_6GHZ, "6 GHz"}, {unix.NL80211_BAND_60GHZ, "60 GHz"},
}

// radio is the capture's account of a wiphy.
func (w *wiphy) radio() *report.WiFiRadio {
	r := &report.WiFiRadio{Bands: []string{}, Source: "nl80211",
		TXChains: bits.OnesCount32(w.tx), RXChains: bits.OnesCount32(w.rx), MaxSpatialStreams: w.streams}
	for _, b := range bandNames {
		if w.bands[b.n] {
			r.Bands = append(r.Bands, b.name)
		}
	}
	switch {
	case w.eht:
		r.Generation = "Wi-Fi 7"
	case w.he && w.bands[unix.NL80211_BAND_6GHZ]:
		r.Generation = "Wi-Fi 6E"
	case w.he:
		r.Generation = "Wi-Fi 6"
	case w.vht:
		r.Generation = "Wi-Fi 5"
	case w.ht:
		r.Generation = "Wi-Fi 4"
	}
	return r
}
