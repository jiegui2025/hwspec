package collect

import (
	"bytes"
	"encoding/binary"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/jiegui2025/hwspec/internal/report"
)

func u16le(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func u32le(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func nest(typ uint16, children ...[]byte) []byte { return attr(typ|0x8000, bytes.Join(children, nil)) }

// nlmsg is a netlink message of type typ answering request seq, with a
// generic netlink header and payload.
func nlmsg(typ uint16, seq uint32, payload []byte) []byte { return nlmsgFlags(typ, 0, seq, payload) }

func nlmsgFlags(typ, flags uint16, seq uint32, payload []byte) []byte {
	b := make([]byte, 20, 20+len(payload))
	binary.LittleEndian.PutUint32(b[0:], uint32(20+len(payload)))
	binary.LittleEndian.PutUint16(b[4:], typ)
	binary.LittleEndian.PutUint16(b[6:], flags)
	binary.LittleEndian.PutUint32(b[8:], seq)
	b = append(b, payload...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func nlerr(seq uint32, errno int32) []byte {
	b := make([]byte, 16, 36)
	binary.LittleEndian.PutUint32(b[0:], 36)
	binary.LittleEndian.PutUint16(b[4:], unix.NLMSG_ERROR)
	binary.LittleEndian.PutUint32(b[8:], seq)
	b = append(b, u32le(uint32(-errno))...)
	return append(b, make([]byte, 16)...) // the request's header
}

func nlctl(typ uint16, seq uint32) []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b[0:], 20)
	binary.LittleEndian.PutUint16(b[4:], typ)
	binary.LittleEndian.PutUint32(b[8:], seq)
	return b
}

func nldone(seq uint32) []byte { return nlctl(unix.NLMSG_DONE, seq) }

// band is a band's attributes; iftype is one interface type's capabilities.
func band(n uint16, as ...[]byte) []byte { return nest(n, as...) }
func iftype(as ...[]byte) []byte         { return nest(unix.NL80211_BAND_ATTR_IFTYPE_DATA, nest(1, as...)) }

func wiphyMsg(index uint32, as ...[]byte) []byte {
	return append(attr(unix.NL80211_ATTR_WIPHY, u32le(index)), bytes.Join(as, nil)...)
}

// Each generation from its highest capability, the bands, chains from the
// antenna masks and streams from the MCS maps; a split dump's messages
// merge by wiphy index (#111).
func TestParseWiphys(t *testing.T) {
	ht := [][]byte{attr(unix.NL80211_BAND_ATTR_HT_CAPA, u16le(0x1ef)), attr(unix.NL80211_BAND_ATTR_HT_MCS_SET, append([]byte{0xff, 0, 0, 0}, make([]byte, 12)...))}
	vht1 := [][]byte{attr(unix.NL80211_BAND_ATTR_VHT_CAPA, u32le(1)), attr(unix.NL80211_BAND_ATTR_VHT_MCS_SET, append(u16le(0xfffe), make([]byte, 6)...))}
	he2 := iftype(attr(unix.NL80211_BAND_IFTYPE_ATTR_HE_CAP_PHY, make([]byte, 11)), attr(unix.NL80211_BAND_IFTYPE_ATTR_HE_CAP_MCS_SET, append(u16le(0xfffa), make([]byte, 10)...)))
	eht := iftype(attr(unix.NL80211_BAND_IFTYPE_ATTR_HE_CAP_PHY, make([]byte, 11)), attr(unix.NL80211_BAND_IFTYPE_ATTR_EHT_CAP_PHY, make([]byte, 9)))
	antennas := func(tx, rx uint32) []byte {
		return append(attr(unix.NL80211_ATTR_WIPHY_ANTENNA_AVAIL_TX, u32le(tx)), attr(unix.NL80211_ATTR_WIPHY_ANTENNA_AVAIL_RX, u32le(rx))...)
	}
	for name, c := range map[string]struct {
		msgs [][]byte
		want report.WiFiRadio
	}{
		"Wi-Fi 4, one stream": {[][]byte{wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_2GHZ, ht...)), antennas(1, 1))},
			report.WiFiRadio{Generation: "Wi-Fi 4", Bands: []string{"2.4 GHz"}, TXChains: 1, RXChains: 1, MaxSpatialStreams: 1}},
		"Wi-Fi 5 1x1": {[][]byte{wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_2GHZ, ht...), band(unix.NL80211_BAND_5GHZ, append(ht, vht1...)...)), antennas(1, 1))},
			report.WiFiRadio{Generation: "Wi-Fi 5", Bands: []string{"2.4 GHz", "5 GHz"}, TXChains: 1, RXChains: 1, MaxSpatialStreams: 1}},
		// The reference AX200, split as the kernel dumps it: a message per band.
		"Wi-Fi 6, split dump": {[][]byte{
			wiphyMsg(0, antennas(3, 3)),
			wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_2GHZ, append(ht, he2)...))),
			wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_5GHZ, append(append(ht, vht1...), he2)...))),
		}, report.WiFiRadio{Generation: "Wi-Fi 6", Bands: []string{"2.4 GHz", "5 GHz"}, TXChains: 2, RXChains: 2, MaxSpatialStreams: 2}},
		"Wi-Fi 6E": {[][]byte{wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_5GHZ, he2), band(unix.NL80211_BAND_6GHZ, he2)), antennas(3, 3))},
			report.WiFiRadio{Generation: "Wi-Fi 6E", Bands: []string{"5 GHz", "6 GHz"}, TXChains: 2, RXChains: 2, MaxSpatialStreams: 2}},
		"Wi-Fi 7": {[][]byte{wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_6GHZ, eht), band(unix.NL80211_BAND_60GHZ)), antennas(3, 3))},
			report.WiFiRadio{Generation: "Wi-Fi 7", Bands: []string{"6 GHz", "60 GHz"}, TXChains: 2, RXChains: 2}},
		// HT's per-stream MCS bytes count when any MCS is set (0x0f: 0–3).
		"Wi-Fi 4, two streams, one partial": {[][]byte{wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_2GHZ,
			attr(unix.NL80211_BAND_ATTR_HT_CAPA, u16le(1)), attr(unix.NL80211_BAND_ATTR_HT_MCS_SET, append([]byte{0xff, 0x0f, 0, 0}, make([]byte, 12)...)))))},
			report.WiFiRadio{Generation: "Wi-Fi 4", Bands: []string{"2.4 GHz"}, MaxSpatialStreams: 2}},
		"no capability": {[][]byte{wiphyMsg(0, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_2GHZ)))},
			report.WiFiRadio{Bands: []string{"2.4 GHz"}}},
	} {
		got := parseWiphys(c.msgs)[0]
		c.want.Source = "nl80211"
		if got == nil || !reflect.DeepEqual(*got.radio(), c.want) {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
	// Messages without a wiphy index, and malformed attributes, are skipped.
	bad := [][]byte{attr(unix.NL80211_ATTR_WIPHY_BANDS, nil), append(wiphyMsg(1), 0xff, 0xff, 0, 0)}
	if got := parseWiphys(bad); len(got) != 1 || got[1] == nil {
		t.Errorf("bad messages: %+v", got)
	}
}

func TestMCSMapStreams(t *testing.T) {
	for m, want := range map[uint16]int{0xffff: 0, 0xfffe: 1, 0xfffa: 2, 0xffea: 3, 0xaaaa: 8, 0xfff3: 2} {
		if got := mcsMapStreams(m); got != want {
			t.Errorf("mcsMapStreams(%#04x) = %d, want %d", m, got, want)
		}
	}
}

// nlConn answers each Write with its next replies, as a netlink socket
// would.
type nlConn struct {
	replies             [][]byte
	sent                [][]byte
	failRead, failWrite error
}

func (s *nlConn) Write(b []byte) (int, error) {
	if s.failWrite != nil {
		return 0, s.failWrite
	}
	s.sent = append(s.sent, append([]byte(nil), b...))
	return len(b), nil
}

func (s *nlConn) Read(b []byte) (int, error) {
	if s.failRead != nil {
		return 0, s.failRead
	}
	if len(s.replies) == 0 {
		return 0, unix.EAGAIN
	}
	n := copy(b, s.replies[0])
	s.replies = s.replies[1:]
	return n, nil
}

func (s *nlConn) Close() error { return nil }

// nl80211ID is the family ID the scripted kernel gives nl80211.
const nl80211ID = 0x1c

func familyReply() []byte {
	return nlmsg(unix.GENL_ID_CTRL, 1, append(attr(unix.CTRL_ATTR_FAMILY_NAME, []byte("nl80211\x00")), attr(unix.CTRL_ATTR_FAMILY_ID, u16le(nl80211ID))...))
}

// vht5 is a 5 GHz VHT band: Wi-Fi 5.
func vht5(index uint32) []byte {
	return wiphyMsg(index, nest(unix.NL80211_ATTR_WIPHY_BANDS, band(unix.NL80211_BAND_5GHZ, attr(unix.NL80211_BAND_ATTR_VHT_CAPA, u32le(1)))))
}

func radios(t *testing.T, c *nlConn, ifaces map[string]int) (map[string]*report.WiFiRadio, error) {
	t.Helper()
	openGenetlink = func() (mgmtConn, error) { return c, nil }
	return nl80211Radios(ifaces)
}

// The exchange: the family by name, then one split dump for every
// interface, each payload kept across reads (#257 B1); no nl80211 is no
// radio and no error; an inconsistent dump is retried once; every other
// failure is an error.
func TestNL80211Radios(t *testing.T) {
	t.Cleanup(saveHooks())
	both := map[string]int{"wlan0": 0, "wlan1": 7, "wlan2": 9}
	// The dump across two reads: wiphy 0, then wiphy 7 and DONE.
	c := &nlConn{replies: [][]byte{familyReply(), nlmsg(nl80211ID, 2, vht5(0)), append(nlmsg(nl80211ID, 2, vht5(7)), nldone(2)...)}}
	got, err := radios(t, c, both)
	if err != nil || len(got) != 2 || got["wlan0"] == nil || got["wlan0"].Generation != "Wi-Fi 5" || got["wlan1"] == nil {
		t.Fatalf("radios %+v, %v", got, err)
	}
	if len(c.sent) != 2 || binary.LittleEndian.Uint16(c.sent[0][4:]) != unix.GENL_ID_CTRL ||
		!bytes.Contains(c.sent[0], []byte("nl80211\x00")) || binary.LittleEndian.Uint16(c.sent[1][4:]) != nl80211ID ||
		binary.LittleEndian.Uint16(c.sent[1][6:]) != unix.NLM_F_REQUEST|unix.NLM_F_DUMP || c.sent[1][16] != unix.NL80211_CMD_GET_WIPHY ||
		binary.LittleEndian.Uint32(c.sent[1][8:]) != 2 {
		t.Errorf("requests %x", c.sent)
	}
	// Replies to other requests, NOOP and an ack are skipped.
	// (A NOOP carrying a wiphy proves it's skipped; the foreign reply comes
	// in its own read, so the family lookup must read on.)
	c = &nlConn{replies: [][]byte{nlmsg(unix.GENL_ID_CTRL, 9, nil), familyReply(),
		bytes.Join([][]byte{nlmsg(unix.NLMSG_NOOP, 2, vht5(9)), nlmsg(nl80211ID, 5, vht5(7)), nlerr(2, 0), nlmsg(nl80211ID, 2, vht5(0)), nldone(2)}, nil)}}
	if got, err := radios(t, c, both); err != nil || len(got) != 1 || got["wlan0"] == nil {
		t.Errorf("other replies: %+v, %v", got, err)
	}
	// An inconsistent dump is read again (request 3).
	intr := nlmsgFlags(nl80211ID, unix.NLM_F_DUMP_INTR, 2, vht5(0))
	c = &nlConn{replies: [][]byte{familyReply(), append(intr, nldone(2)...), append(nlmsg(nl80211ID, 3, vht5(0)), nldone(3)...)}}
	if got, err := radios(t, c, both); err != nil || got["wlan0"] == nil || len(c.sent) != 3 {
		t.Errorf("retried dump: %+v, %v, %d requests", got, err, len(c.sent))
	}
	intr3 := nlmsgFlags(nl80211ID, unix.NLM_F_DUMP_INTR, 3, vht5(0))
	c = &nlConn{replies: [][]byte{familyReply(), append(intr, nldone(2)...), append(intr3, nldone(3)...)}}
	if _, err := radios(t, c, both); err == nil || !strings.Contains(err.Error(), "changed while it was read, twice") {
		t.Errorf("twice inconsistent: %v", err)
	}
	if got, err := radios(t, &nlConn{replies: [][]byte{nlerr(1, int32(unix.ENOENT))}}, both); got != nil || err != nil {
		t.Errorf("no nl80211: %+v, %v", got, err)
	}
	for name, c := range map[string]*nlConn{
		"refused":         {replies: [][]byte{nlerr(1, int32(unix.EPERM))}},
		"no family ID":    {replies: [][]byte{nlmsg(unix.GENL_ID_CTRL, 1, nil)}},
		"read fails":      {failRead: unix.EIO},
		"write fails":     {failWrite: unix.EIO},
		"ENOENT in dump":  {replies: [][]byte{familyReply(), nlerr(2, int32(unix.ENOENT))}},
		"overrun":         {replies: [][]byte{familyReply(), append(nlctl(unix.NLMSG_OVERRUN, 2), nldone(2)...)}},
		"dump never ends": {replies: [][]byte{familyReply(), nlmsg(nl80211ID, 2, vht5(0))}},
		"truncated":       {replies: [][]byte{familyReply()[:24]}},
		"truncated error": {replies: [][]byte{nlerr(1, int32(unix.EIO))[:18]}},
	} {
		if r, err := radios(t, c, both); err == nil {
			t.Errorf("%s: %+v, no error", name, r)
		}
	}
	openGenetlink = func() (mgmtConn, error) { return nil, errors.New("no socket") }
	if _, err := nl80211Radios(both); err == nil || !strings.Contains(err.Error(), "no socket") {
		t.Errorf("socket: %v", err)
	}
}

// #257 B2: a length that runs past the buffer's end ends the parse; it
// doesn't panic.
func TestSplitMessagesClampsTheLastLength(t *testing.T) {
	var out [][]byte
	in := []byte("\x11\x00\x00\x00" + strings.Repeat("\x00", 13))
	if _, _, err := splitMessages(in, 0, &out); err != nil {
		t.Errorf("%v", err)
	}
}

func FuzzParseNetlink(f *testing.F) {
	f.Add(append(familyReply(), nlmsg(nl80211ID, 2, vht5(0))...))
	f.Add(nlerr(1, int32(unix.ENOENT)))
	f.Add([]byte("\x11\x00\x00\x00" + strings.Repeat("\x00", 13)))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, seq := range []uint32{0, 1, 2} {
			var out [][]byte
			splitMessages(b, seq, &out)
			for _, w := range parseWiphys(out) {
				w.radio()
			}
		}
		attrs(b)
	})
}

// The collector: radios for the wireless NICs with a phy index, from one
// exchange; a warning when it fails or doesn't answer in time (#257 I1);
// nothing for a NIC without a phy or a radio nl80211 doesn't know.
func TestWiFiRadioInTheCapture(t *testing.T) {
	file, link := fakeRoot(t)
	for _, n := range []string{"wlan0", "wlan1", "wlan2"} {
		link("/sys/class/net/"+n, "../../devices/virtual/net/"+n)
		file("/sys/devices/virtual/net/"+n+"/operstate", "up")
		file("/sys/devices/virtual/net/"+n+"/wireless/x", "")
		file("/sys/devices/virtual/net/"+n+"/device/vendor", "0x8086") // a backing device, as physical NICs have
	}
	file("/sys/devices/virtual/net/wlan0/phy80211/index", "0")
	file("/sys/devices/virtual/net/wlan1/phy80211/index", "1")
	ethtoolDrvinfo = func(string) (string, error) { return "", unix.EOPNOTSUPP }
	var asked map[string]int
	readRadios = func(ifaces map[string]int) (map[string]*report.WiFiRadio, error) {
		asked = maps.Clone(ifaces)
		return map[string]*report.WiFiRadio{"wlan0": {Generation: "Wi-Fi 6", Bands: []string{"2.4 GHz"}, Source: "nl80211"}}, nil
	}
	c := &collector{r: &report.Report{}}
	c.network()
	got := map[string]*report.WiFiRadio{}
	for _, n := range c.r.Network {
		got[n.Name] = n.Radio
	}
	if !maps.Equal(asked, map[string]int{"wlan0": 0, "wlan1": 1}) || got["wlan0"] == nil || got["wlan0"].Generation != "Wi-Fi 6" ||
		got["wlan1"] != nil || got["wlan2"] != nil || len(c.r.Warnings) != 0 {
		t.Errorf("asked %v, radios %+v, warnings %q", asked, got, c.r.Warnings)
	}

	readRadios = func(map[string]int) (map[string]*report.WiFiRadio, error) { return nil, unix.EBUSY }
	c = &collector{r: &report.Report{}}
	c.network()
	if !hasWarning(c.r, "network: Wi-Fi radios (nl80211): device or resource busy") || len(c.r.Warnings) != 1 {
		t.Errorf("an error: %q", c.r.Warnings)
	}
	shortAnswer(t)
	readRadios = func(map[string]int) (map[string]*report.WiFiRadio, error) { select {} }
	c = &collector{r: &report.Report{}}
	c.network()
	if !hasWarning(c.r, "network: Wi-Fi radios (nl80211): didn't answer within 50ms; left out") {
		t.Errorf("no answer: %q", c.r.Warnings)
	}
}
