package ids

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// NormalizeKey turns an ID as a person writes it into the key a database
// stores, so the overrides file and `hwspec ids lookup` accept the same
// spellings: "46d" and "046D" are both USB vendor "046d", "2" and "0x0002"
// are both Bluetooth company "0002", and JEDEC IDs may include the parity
// bit ("1:CE" is "1:4E", Samsung).
func NormalizeKey(kind Kind, id string) (string, error) {
	id = strings.TrimSpace(id)
	switch kind {
	case PCI, USB:
		if code, isClass := strings.CutPrefix(strings.ToLower(id), "class "); isClass {
			code = norm(code)
			if kind == PCI && len(code) == 6 && isHex(code) {
				return "", fmt.Errorf("%q: names are per class or subclass; drop the programming interface (use %s)", code, code[:4])
			}
			if !isHex(code) || len(code)%2 != 0 || len(code) > 4 || (kind == USB && len(code) != 2) {
				return "", fmt.Errorf("%q is not a %s class code", strings.TrimSpace(code), kind)
			}
			return "class:" + code, nil
		}
		parts := strings.Split(id, ":")
		if valid := len(parts) == 1 || len(parts) == 2 || (kind == PCI && len(parts) == 4); !valid {
			return "", fmt.Errorf("%q: want VENDOR, VENDOR:DEVICE%s", id, map[bool]string{true: " or VENDOR:DEVICE:SUBVENDOR:SUBDEVICE", false: ""}[kind == PCI])
		}
		for i, p := range parts {
			h, err := hexID(p, 4)
			if err != nil {
				return "", fmt.Errorf("%q: %w", id, err)
			}
			parts[i] = h
		}
		return strings.Join(parts, ":"), nil
	case PNP:
		k := strings.ToUpper(id)
		if len(k) != 3 || strings.IndexFunc(k, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			return "", fmt.Errorf("%q is not a 3-letter PNP ID", id)
		}
		return k, nil
	case OUI:
		prefix, ok := ouiPrefix(id)
		if !ok {
			return "", fmt.Errorf("%q is not a MAC address or prefix", id)
		}
		return prefix, nil
	case JEDEC:
		if b, code, isPair := strings.Cut(id, ":"); isPair {
			bank, err1 := strconv.Atoi(strings.TrimSpace(b))
			v, err2 := strconv.ParseUint(norm(code), 16, 8)
			if err1 != nil || err2 != nil || bank < 1 || bank > maxJEDECBank || !validID(int(v)) {
				return "", fmt.Errorf("%q: want BANK:ID with bank 1-%d and a hex ID 01-7E (parity bit allowed)", id, maxJEDECBank)
			}
			return jedecKey(bank, int(v)&0x7F), nil
		}
		c := jedecCandidates(id)
		if len(c) == 0 {
			return "", fmt.Errorf("%q is not a JEDEC code", id)
		}
		return c[0], nil
	case AMDGPU:
		dev, rev, ok := strings.Cut(id, ":")
		if !ok {
			return "", fmt.Errorf("%q: want DEVICE:REVISION, e.g. 1114:c2", id)
		}
		d, err1 := hexID(dev, 4)
		r, err2 := hexID(rev, 2)
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("%q: want hex DEVICE:REVISION, e.g. 1114:c2", id)
		}
		return d + ":" + r, nil
	case BT:
		v, err := strconv.ParseUint(norm(id), 16, 16)
		if err != nil {
			return "", fmt.Errorf("%q is not a hex company ID (0000-FFFF)", id)
		}
		return fmt.Sprintf("%04X", v), nil
	case CPU:
		f := strings.Split(strings.ToLower(id), ":")
		if len(f) != 3 && len(f) != 4 || (f[0] != "intel" && f[0] != "amd") {
			return "", fmt.Errorf("%q: want intel|amd:FAMILY:MODEL[:STEPPING], family and model in hex", id)
		}
		family, err1 := strconv.ParseUint(norm(f[1]), 16, 16)
		model, err2 := strconv.ParseUint(norm(f[2]), 16, 16)
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("%q: family and model must be hex", id)
		}
		key := fmt.Sprintf("%s:%x:%02x", f[0], family, model)
		if len(f) == 4 {
			s, err := strconv.Atoi(f[3])
			if err != nil || s < 0 {
				return "", fmt.Errorf("%q: stepping must be a decimal number", id)
			}
			key += ":" + strconv.Itoa(s)
		}
		return key, nil
	}
	return "", fmt.Errorf("unknown database %q (want one of %s)", kind, kindList())
}

func hexID(s string, width int) (string, error) {
	s = norm(s)
	if s == "" || len(s) > width || !isHex(s) {
		return "", fmt.Errorf("%q is not a hex ID of up to %d digits", s, width)
	}
	return strings.Repeat("0", width-len(s)) + s, nil
}

func isHex(s string) bool {
	_, err := strconv.ParseUint(s, 16, 64)
	return s != "" && err == nil
}

// cleanName makes a name from a database, a device or a file safe to show
// in a terminal: control characters (which could carry escape sequences)
// are dropped and invalid UTF-8 is replaced.
func cleanName(s string) string {
	if printableASCII(s) { // nearly every database name: skip the rune walk
		return strings.TrimSpace(s)
	}
	// Control characters carry escape sequences; Cf (format) characters
	// include bidi overrides that visually reorder text.
	unsafe := func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }
	s = strings.ToValidUTF8(s, "\uFFFD")
	if strings.IndexFunc(s, unsafe) < 0 {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unsafe(r) {
			return -1
		}
		return r
	}, s))
}

// printableASCII reports whether s is only bytes 0x20-0x7E: valid UTF-8
// with no control or format characters, which cleanName would keep as is.
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// CleanName is cleanName for other packages (device strings, captures).
func CleanName(s string) string { return cleanName(s) }
