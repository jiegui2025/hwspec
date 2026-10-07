// Package tpm builds the one TPM 2.0 command hwspec sends, and reads its
// reply: TPM2_GetCapability for the TPM's fixed properties, which name its
// manufacturer and firmware version (#122). It is pure: the caller writes
// the command to the kernel's resource-managed device (/dev/tpmrm0) and
// passes the reply in.
//
// The layout is the TCG TPM 2.0 Library's, as its reference implementation
// (github.com/microsoft/ms-tpm-20-ref, BSD; TpmTypes.h and
// GetCapability_fp.h are generated from the specification's tables)
// reads and writes it. All numbers are big-endian.
//
//   - Headers (ExecCommand.c, Response.c): a command is tag (TPM_ST, 2
//     bytes), size (UINT32, the whole command) and command code (TPM_CC,
//     UINT32), then its parameters; a reply is tag, size and response code
//     (TPM_RC, UINT32, 0 for success), then its parameters. A failed
//     command's reply is the 10-byte header alone.
//   - Part 2, Structures, table "Definition of TPM_ST Constants":
//     TPM_ST_NO_SESSIONS = 0x8001. "Definition of TPM_CC Constants":
//     TPM_CC_GetCapability = 0x0000017A. "Definition of TPM_CAP
//     Constants": TPM_CAP_TPM_PROPERTIES = 0x00000006. "Definition of
//     TPM_PT Constants": PT_FIXED = 0x100, TPM_PT_MANUFACTURER = PT_FIXED +
//     5, TPM_PT_VENDOR_STRING_1..4 = PT_FIXED + 6..9, TPM_PT_FIRMWARE_
//     VERSION_1 and _2 = PT_FIXED + 11 and 12.
//   - Part 3, Commands, TPM2_GetCapability: parameters capability (TPM_CAP),
//     property (UINT32, the first one wanted) and propertyCount (UINT32);
//     reply moreData (TPMI_YES_NO, one byte), then capabilityData
//     (TPMS_CAPABILITY_DATA: capability, then for TPM_CAP_TPM_PROPERTIES a
//     TPML_TAGGED_TPM_PROPERTY: count, then count TPMS_TAGGED_PROPERTY
//     {property, value}).
package tpm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// The constants above, by their specification names.
const (
	stNoSessions      = 0x8001
	ccGetCapability   = 0x0000017A
	capTPMProperties  = 0x00000006
	ptManufacturer    = 0x100 + 5
	ptVendorString1   = 0x100 + 6
	ptFirmwareVersion = 0x100 + 11 // _1; _2 follows
	// propertyCount covers TPM_PT_MANUFACTURER to TPM_PT_FIRMWARE_VERSION_2.
	propertyCount = ptFirmwareVersion + 1 - ptManufacturer + 1
	headerSize    = 10
)

// Command is TPM2_GetCapability for the fixed properties from
// TPM_PT_MANUFACTURER to TPM_PT_FIRMWARE_VERSION_2: 22 bytes.
func Command() []byte {
	b := make([]byte, 0, headerSize+12)
	b = binary.BigEndian.AppendUint16(b, stNoSessions)
	b = binary.BigEndian.AppendUint32(b, headerSize+12)
	b = binary.BigEndian.AppendUint32(b, ccGetCapability)
	b = binary.BigEndian.AppendUint32(b, capTPMProperties)
	b = binary.BigEndian.AppendUint32(b, ptManufacturer)
	return binary.BigEndian.AppendUint32(b, propertyCount)
}

// Info is what the reply names.
type Info struct {
	// Manufacturer is the TPM_PT_MANUFACTURER bytes as text, e.g. "IFX",
	// cleaned as systemd's tpm2_id cleans them, so it reads the same as
	// udev's ID_TPM2_MODALIAS "mf" field.
	Manufacturer string
	// VendorString is TPM_PT_VENDOR_STRING_1..4 as text, e.g. "SLB9670".
	VendorString string
	// Firmware1 and Firmware2 are TPM_PT_FIRMWARE_VERSION_1 and _2, whose
	// meaning the vendor defines.
	Firmware1, Firmware2 uint32
}

// FirmwareVersion writes the two firmware words the way systemd's tpm2_id
// does in udev's ID_TPM2_MODALIAS: the first as two 16-bit halves, then
// the second whole ("7.85.1166080"), so a capture reads the same whichever
// of the two it came from. fwupd shows the same words as four 16-bit
// parts ("7.85.17.51968").
func (i Info) FirmwareVersion() string {
	return fmt.Sprintf("%d.%d.%d", i.Firmware1>>16, i.Firmware1&0xFFFF, i.Firmware2)
}

// Parse reads the reply to Command. A reply that isn't one, a TPM error,
// or one without the manufacturer and both firmware words, is an error.
func Parse(reply []byte) (Info, error) {
	if len(reply) < headerSize {
		return Info{}, fmt.Errorf("reply of %d bytes is shorter than a header", len(reply))
	}
	tag := binary.BigEndian.Uint16(reply)
	size := binary.BigEndian.Uint32(reply[2:])
	rc := binary.BigEndian.Uint32(reply[6:])
	switch {
	case tag != stNoSessions:
		return Info{}, fmt.Errorf("reply tag 0x%04x, want 0x%04x", tag, stNoSessions)
	case uint64(size) != uint64(len(reply)):
		return Info{}, fmt.Errorf("reply says %d bytes, has %d", size, len(reply))
	case rc != 0:
		return Info{}, fmt.Errorf("TPM response code 0x%08x", rc)
	}
	body := reply[headerSize:]
	// moreData (1), capability (4), count (4).
	if len(body) < 9 {
		return Info{}, errors.New("reply is too short for its capability data")
	}
	if c := binary.BigEndian.Uint32(body[1:]); c != capTPMProperties {
		return Info{}, fmt.Errorf("reply is for capability 0x%x, want 0x%x", c, capTPMProperties)
	}
	count := binary.BigEndian.Uint32(body[5:])
	props := body[9:]
	if uint64(count)*8 != uint64(len(props)) {
		return Info{}, fmt.Errorf("reply lists %d properties in %d bytes", count, len(props))
	}
	values := map[uint32]uint32{}
	for p := props; len(p) >= 8; p = p[8:] {
		values[binary.BigEndian.Uint32(p)] = binary.BigEndian.Uint32(p[4:])
	}
	var info Info
	var missing []string
	need := func(pt uint32, name string) uint32 {
		v, ok := values[pt]
		if !ok {
			missing = append(missing, name)
		}
		return v
	}
	info.Manufacturer = text(need(ptManufacturer, "TPM_PT_MANUFACTURER"))
	info.Firmware1 = need(ptFirmwareVersion, "TPM_PT_FIRMWARE_VERSION_1")
	info.Firmware2 = need(ptFirmwareVersion+1, "TPM_PT_FIRMWARE_VERSION_2")
	if len(missing) > 0 {
		return Info{}, fmt.Errorf("reply doesn't give %s", strings.Join(missing, ", "))
	}
	if info.Manufacturer == "" {
		return Info{}, errors.New("TPM_PT_MANUFACTURER is empty")
	}
	words := make([]uint32, 4)
	for i := range words {
		words[i] = values[ptVendorString1+uint32(i)]
	}
	info.VendorString = text(words...)
	return info, nil
}

// text turns property words into text as systemd's tpm2_id does
// (mangle_vendor_chars): the big-endian bytes, trailing spaces and NULs
// dropped, then every byte that is a control character, a space or not
// ASCII written as "_".
func text(words ...uint32) string {
	b := make([]byte, 0, 4*len(words))
	for _, w := range words {
		b = binary.BigEndian.AppendUint32(b, w)
	}
	end := 0
	for i, c := range b {
		if c != ' ' && c != 0 {
			end = i + 1
		}
	}
	b = b[:end]
	for i, c := range b {
		if c <= ' ' || c >= 127 {
			b[i] = '_'
		}
	}
	return string(b)
}
