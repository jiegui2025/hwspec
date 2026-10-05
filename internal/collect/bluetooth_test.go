package collect

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// scriptedConn plays the kernel's side of the management socket.
type scriptedConn struct {
	written  []byte
	replies  [][]byte
	writeErr error
	closed   bool
}

func (c *scriptedConn) Write(b []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.written = append(c.written, b...)
	return len(b), nil
}

func (c *scriptedConn) Read(b []byte) (int, error) {
	if len(c.replies) == 0 {
		return 0, errors.New("resource temporarily unavailable")
	}
	n := copy(b, c.replies[0])
	c.replies = c.replies[1:]
	return n, nil
}

func (c *scriptedConn) Close() error { c.closed = true; return nil }

func event(code, index, opcode uint16, status byte, payload []byte) []byte {
	b := make([]byte, 9, 9+len(payload))
	binary.LittleEndian.PutUint16(b[0:], code)
	binary.LittleEndian.PutUint16(b[2:], index)
	binary.LittleEndian.PutUint16(b[6:], opcode)
	b[8] = status
	return append(b, payload...)
}

// Read Info sends one command for the asked controller, skips events for
// others, and stops at its own reply, a failure status, or after 16
// unrelated events.
func TestBluetoothReadInfoConversation(t *testing.T) {
	t.Cleanup(saveHooks())
	answer := make([]byte, readInfoReplyBytes)
	copy(answer, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
	answer[6] = 12 // 5.3
	copy(answer[20:], "box\x00")
	var conn *scriptedConn
	openMgmt = func() (mgmtConn, error) { return conn, nil }

	conn = &scriptedConn{replies: [][]byte{
		event(evCmdComplete, 3, opReadInfo, 0, answer), // another controller
		event(0x0006, 1, 0, 0, nil),                    // an unrelated event
		event(evCmdComplete, 1, opReadInfo, 0, answer),
	}}
	info, err := mgmtReadInfo(1)
	if err != nil || info.address != "06:05:04:03:02:01" || btVersion(info.version) != "5.3" || info.name != "box" {
		t.Errorf("info = %+v, %v", info, err)
	}
	if !conn.closed || binary.LittleEndian.Uint16(conn.written[0:]) != opReadInfo || binary.LittleEndian.Uint16(conn.written[2:]) != 1 {
		t.Errorf("command % x, closed %v", conn.written, conn.closed)
	}

	conn = &scriptedConn{replies: [][]byte{event(evCmdStatus, 0, opReadInfo, 0x11, nil)}}
	if _, err := mgmtReadInfo(0); err == nil || err.Error() != "management status 17" {
		t.Errorf("invalid index: %v", err)
	}
	conn = &scriptedConn{}
	if _, err := mgmtReadInfo(0); err == nil || !strings.Contains(err.Error(), "management reply") {
		t.Errorf("no reply: %v", err)
	}
	conn = &scriptedConn{writeErr: errors.New("broken pipe")}
	if _, err := mgmtReadInfo(0); err == nil || !strings.Contains(err.Error(), "management command") {
		t.Errorf("write failure: %v", err)
	}
	conn = &scriptedConn{}
	for range 16 {
		conn.replies = append(conn.replies, event(0x0006, 0, 0, 0, nil))
	}
	if _, err := mgmtReadInfo(0); err == nil || err.Error() != "no management reply" {
		t.Errorf("only noise: %v", err)
	}
	openMgmt = func() (mgmtConn, error) { return nil, errors.New("management socket: address family not supported") }
	if _, err := mgmtReadInfo(0); err == nil {
		t.Error("no socket, yet an answer")
	}
}
