package collect

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/jiegui2025/hwspec/internal/report"
)

// hciEvent builds an HCI event packet.
func hciEvent(code byte, params ...byte) []byte {
	return append([]byte{hciEventPkt, code, byte(len(params))}, params...)
}

// complete is a Command Complete event for opcode with return parameters.
func complete(opcode uint16, ret ...byte) []byte {
	p := []byte{1, 0, 0}
	binary.LittleEndian.PutUint16(p[1:], opcode)
	return hciEvent(evtCmdComplete, append(p, ret...)...)
}

// ax200 is the reference machine's AX200 reply: hci_ver 11, hci_rev
// 0x21c1, lmp_ver 11, manufacturer 2 (Intel), lmp_subver 0x21c1.
var ax200 = complete(opReadLocalVer, 0, 11, 0xc1, 0x21, 11, 2, 0, 0xc1, 0x21)

func TestParseLocalVersion(t *testing.T) {
	v, err := parseLocalVersion(ax200)
	if err != nil || *v != (hciVersion{hciVersion: 11, hciRevision: 0x21c1, lmpVersion: 11, manufacturer: 2, lmpSubver: 0x21c1}) {
		t.Errorf("%+v, %v", v, err)
	}
	// Every field from its own bytes.
	v, err = parseLocalVersion(complete(opReadLocalVer, 0, 9, 0x34, 0x12, 10, 0x0f, 0x00, 0x78, 0x56))
	if err != nil || *v != (hciVersion{hciVersion: 9, hciRevision: 0x1234, lmpVersion: 10, manufacturer: 15, lmpSubver: 0x5678}) {
		t.Errorf("%+v, %v", v, err)
	}
	status := func(st byte, opcode uint16) []byte {
		p := []byte{st, 1, 0, 0}
		binary.LittleEndian.PutUint16(p[2:], opcode)
		return hciEvent(evtCmdStatus, p...)
	}
	for name, c := range map[string]struct {
		pkt  []byte
		want string // "" for an unrelated packet
	}{
		"not an event":     {append([]byte{0x02}, ax200[1:]...), ""},
		"too short":        {[]byte{hciEventPkt, evtCmdComplete}, ""},
		"length mismatch":  {append(bytes.Clone(ax200), 0), ""},
		"another event":    {hciEvent(0x05, 0, 1, 0, 0x13), ""},
		"another command":  {complete(0x0c03, 0), ""},
		"short complete":   {hciEvent(evtCmdComplete, 1, 0x01), ""},
		"pending status":   {status(0, opReadLocalVer), ""},
		"another's status": {status(1, 0x0c03), ""},
		"short status":     {hciEvent(evtCmdStatus, 1, 1), ""},
		"refused":          {status(0x01, opReadLocalVer), "HCI command status 0x01"},
		"failed":           {complete(opReadLocalVer, 0x0c), "HCI status 0x0c"},
		"no status":        {complete(opReadLocalVer), "HCI reply without a status"},
		"short reply":      {complete(opReadLocalVer, 0, 11, 0xc1), "HCI reply of 3 bytes, want 9"},
		"one byte short":   {complete(opReadLocalVer, 0, 11, 0xc1, 0x21, 11, 2, 0, 0xc1), "HCI reply of 8 bytes, want 9"},
	} {
		v, err := parseLocalVersion(c.pkt)
		switch {
		case c.want == "" && (v != nil || err != nil):
			t.Errorf("%s: %+v, %v; want it skipped", name, v, err)
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The command goes out once on the asked controller's socket; the reply is
// found among other events, and every failure says where it happened.
func TestHCIReadLocalVersionConversation(t *testing.T) {
	t.Cleanup(saveHooks())
	var conn *scriptedConn
	var asked uint16
	openHCI = func(dev uint16) (mgmtConn, error) { asked = dev; return conn, nil }

	conn = &scriptedConn{replies: [][]byte{hciEvent(0x05, 0, 1, 0, 0x13), complete(0x0c03, 0), ax200}}
	v, err := hciReadLocalVersion(2)
	if err != nil || v.lmpSubver != 0x21c1 || asked != 2 || !conn.closed || !bytes.Equal(conn.written, []byte{0x01, 0x01, 0x10, 0x00}) {
		t.Errorf("%+v, %v, dev %d, command % x, closed %v", v, err, asked, conn.written, conn.closed)
	}
	conn = &scriptedConn{writeErr: unix.ENETDOWN}
	if _, err := hciReadLocalVersion(0); !errors.Is(err, unix.ENETDOWN) || !strings.HasPrefix(err.Error(), "HCI command: ") {
		t.Errorf("down: %v", err)
	}
	conn = &scriptedConn{}
	if _, err := hciReadLocalVersion(0); err == nil || !strings.HasPrefix(err.Error(), "HCI reply: ") {
		t.Errorf("silence: %v", err)
	}
	conn = &scriptedConn{}
	for range 16 {
		conn.replies = append(conn.replies, hciEvent(0x05, 0, 1, 0, 0x13))
	}
	if _, err := hciReadLocalVersion(0); err == nil || err.Error() != "no HCI reply" {
		t.Errorf("only noise: %v", err)
	}
	conn = &scriptedConn{replies: [][]byte{complete(opReadLocalVer, 0x0c)}}
	if _, err := hciReadLocalVersion(0); err == nil || err.Error() != "HCI status 0x0c" {
		t.Errorf("failure status: %v", err)
	}
	openHCI = func(uint16) (mgmtConn, error) { return nil, errors.New("HCI socket: address family not supported") }
	if _, err := hciReadLocalVersion(0); err == nil {
		t.Error("no socket, yet an answer")
	}
}

// The firmware block, or why there is none: a controller that is down and
// a refused command are named; every failure is also a warning.
func TestBluetoothFirmware(t *testing.T) {
	t.Cleanup(saveHooks())
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, "0x5678 0x1234 hci"},
		{fmt.Errorf("HCI command: %w", unix.ENETDOWN), "unknown: the controller is down (powered off or blocked)"},
		{fmt.Errorf("HCI command: %w", unix.EPERM), "unknown: the kernel refused the HCI command"},
		{fmt.Errorf("HCI socket: %w", unix.EACCES), "unknown: the kernel refused the HCI command"},
		{errors.New("no HCI reply"), "unknown: the controller didn't answer HCI Read Local Version"},
	} {
		readBTVersion = func(uint16) (*hciVersion, error) {
			if c.err != nil {
				return nil, c.err
			}
			return &hciVersion{hciRevision: 0x1234, lmpSubver: 0x5678}, nil
		}
		col := &collector{r: &report.Report{}}
		fw := col.btFirmware("hci0", 0)
		got := "unknown: " + fw.Reason
		if fw.Known() {
			got = fw.Version + " " + fw.Release + " " + fw.Source
		}
		w := strings.Join(col.r.Warnings, "\n")
		if got != c.want || (c.err == nil) != (w == "") || c.err != nil && !strings.Contains(w, "bluetooth hci0: firmware version: ") {
			t.Errorf("%v: %q, warnings %q", c.err, got, w)
		}
	}
}

// Replies come from a controller: parseLocalVersion must handle anything
// without panicking, and only a well-formed reply to its command gives a
// version.
func FuzzParseLocalVersion(f *testing.F) {
	f.Add(ax200)
	f.Add(complete(opReadLocalVer, 0x0c))
	f.Add(hciEvent(evtCmdStatus, 1, 1, 0x01, 0x10))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, pkt []byte) {
		v, err := parseLocalVersion(pkt)
		if v == nil {
			return
		}
		if err != nil || len(pkt) < 3+3+localVersionBytes || pkt[1] != evtCmdComplete || pkt[6] != 0 {
			t.Fatalf("version %+v from % x", v, pkt)
		}
	})
}
