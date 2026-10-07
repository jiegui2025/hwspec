package tpm

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// recorded is the reference machine's reply to Command (HP EliteDesk 800
// G5 Mini, Infineon SLB 9670), read as root from /dev/tpmrm0 on
// 2026-10-06. moreData is 1: the TPM has properties past the eight asked
// for.
const recorded = "" +
	"8001 00000053 00000000" + // header: 83 bytes, success
	"01 00000006 00000008" + // moreData, TPM_CAP_TPM_PROPERTIES, 8 properties
	"00000105 49465800" + // TPM_PT_MANUFACTURER "IFX\0"
	"00000106 534c4239" + // TPM_PT_VENDOR_STRING_1 "SLB9"
	"00000107 36373000" + // _2 "670\0"
	"00000108 00000000" + // _3
	"00000109 00000000" + // _4
	"0000010a 00000000" + // TPM_PT_VENDOR_TPM_TYPE
	"0000010b 00070055" + // TPM_PT_FIRMWARE_VERSION_1
	"0000010c 0011cb00" // _2

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTheCommandIsGetCapability(t *testing.T) {
	want := unhex(t, "8001 00000016 0000017a 00000006 00000105 00000008")
	if got := Command(); !bytes.Equal(got, want) {
		t.Errorf("command % x, want % x", got, want)
	}
}

// The recorded reply reads as udev (systemd's tpm2_id) and fwupd read the
// same TPM: ID_TPM2_MODALIAS "mfIFX:vsSLB9670:…:fw7.85.1166080:", and
// fwupdmgr's "Infineon" TPM at 7.85.17.51968, the same two words.
func TestTheRecordedReply(t *testing.T) {
	info, err := Parse(unhex(t, recorded))
	if err != nil {
		t.Fatal(err)
	}
	if info.Manufacturer != "IFX" || info.VendorString != "SLB9670" || info.FirmwareVersion() != "7.85.1166080" {
		t.Errorf("%+v, firmware %s", info, info.FirmwareVersion())
	}
	quad := []uint32{info.Firmware1 >> 16, info.Firmware1 & 0xFFFF, info.Firmware2 >> 16, info.Firmware2 & 0xFFFF}
	if quad[0] != 7 || quad[1] != 85 || quad[2] != 17 || quad[3] != 51968 {
		t.Errorf("fwupd would read %v", quad)
	}
}

// Each half of the first word is 16 bits wide.
func TestFirmwareVersion(t *testing.T) {
	if got := (Info{Firmware1: 0x01020304, Firmware2: 0xFFFFFFFF}).FirmwareVersion(); got != "258.772.4294967295" {
		t.Errorf("version %s", got)
	}
}

// reply builds a reply with the given properties.
func reply(props ...uint32) []byte {
	b := binary.BigEndian.AppendUint16(nil, stNoSessions)
	b = binary.BigEndian.AppendUint32(b, uint32(headerSize+9+4*len(props)))
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, 0)
	b = binary.BigEndian.AppendUint32(b, capTPMProperties)
	b = binary.BigEndian.AppendUint32(b, uint32(len(props)/2))
	for _, p := range props {
		b = binary.BigEndian.AppendUint32(b, p)
	}
	return b
}

func TestMalformedReplies(t *testing.T) {
	good := unhex(t, recorded)
	set := func(b []byte, at int, v uint32) []byte {
		b = bytes.Clone(b)
		binary.BigEndian.PutUint32(b[at:], v)
		return b
	}
	badTag := bytes.Clone(good)
	badTag[0] = 0x80
	badTag[1] = 0x02
	for name, c := range map[string]struct {
		b    []byte
		want string
	}{
		"empty":            {nil, "reply of 0 bytes is shorter than a header"},
		"a TPM error":      {unhex(t, "8001 0000000a 00000101"), "TPM response code 0x00000101"},
		"another tag":      {badTag, "reply tag 0x8002, want 0x8001"},
		"truncated":        {good[:len(good)-4], "reply says 83 bytes, has 79"},
		"no body":          {unhex(t, "8001 0000000e 00000000 01000000"), "too short for its capability data"},
		"part of a body":   {unhex(t, "8001 00000012 00000000 01000000 06000000"), "too short for its capability data"},
		"trailing bytes":   {append(set(good, 2, 84), 0), "reply lists 8 properties in 65 bytes"},
		"longer than said": {append(bytes.Clone(good), 0), "reply says 83 bytes, has 84"},
		"other capability": {set(good, 11, 1), "reply is for capability 0x1, want 0x6"},
		"wrong count":      {set(good, 15, 9), "reply lists 9 properties in 64 bytes"},
		"huge count":       {set(good, 15, 0xFFFFFFFF), "reply lists 4294967295 properties in 64 bytes"},
		"no firmware":      {reply(ptManufacturer, 0x49465800), "reply doesn't give TPM_PT_FIRMWARE_VERSION_1, TPM_PT_FIRMWARE_VERSION_2"},
		"no manufacturer":  {reply(ptFirmwareVersion, 1, ptFirmwareVersion+1, 2), "reply doesn't give TPM_PT_MANUFACTURER"},
		"empty manufacturer": {reply(ptManufacturer, 0x20000000, ptFirmwareVersion, 1, ptFirmwareVersion+1, 2),
			"TPM_PT_MANUFACTURER is empty"},
	} {
		if _, err := Parse(c.b); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// Text is cleaned as systemd cleans it: trailing spaces and NULs go, and
// what's left that isn't printable ASCII becomes "_".
func TestText(t *testing.T) {
	for want, words := range map[string][]uint32{
		"IFX":     {0x49465800},
		"NTC":     {0x4e544320},
		"A_B":     {0x41004200},
		"X__":     {0x58ff0a00},
		"":        {0, 0x20202020},
		"SLB9670": {0x534c4239, 0x36373000, 0, 0},
		"AB_CD":   {0x41422043, 0x44000000},
	} {
		if got := text(words...); got != want {
			t.Errorf("%x: %q, want %q", words, got, want)
		}
	}
}

// Replies come from a device: Parse must handle anything without
// panicking, and what it accepts it reads back the same from a rebuilt
// reply.
func FuzzParse(f *testing.F) {
	f.Add(unhex(f, recorded))
	f.Add(reply(ptManufacturer, 0x49465800, ptFirmwareVersion, 1, ptFirmwareVersion+1, 2))
	f.Add([]byte{0x80, 0x01, 0, 0, 0, 10, 0, 0, 1, 1})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := Parse(b)
		if err != nil {
			return
		}
		if info.Manufacturer == "" || strings.ContainsFunc(info.Manufacturer+info.VendorString, func(r rune) bool { return r <= ' ' || r >= 127 }) {
			t.Fatalf("unclean text %+v", info)
		}
		var words []uint32
		for p := b[headerSize+9:]; len(p) >= 8; p = p[8:] {
			words = append(words, binary.BigEndian.Uint32(p), binary.BigEndian.Uint32(p[4:]))
		}
		again, err := Parse(reply(words...))
		if err != nil || again != info {
			t.Fatalf("rebuilt reply: %+v, %v; want %+v", again, err, info)
		}
	})
}
