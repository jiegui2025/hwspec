package collect

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/jiegui2025/hwspec/internal/report"
)

// diskHealth reads SMART data (root only). NVMe drives are queried directly
// with the kernel's admin-command ioctl; other drives use smartctl when it's
// installed.
func (c *collector) diskHealth(name, transport string) *report.DiskHealth {
	if transport == "nvme" {
		ctrl := nvmeCtrl.FindString(name)
		h, err := nvmeHealth("/dev/" + ctrl)
		if err != nil {
			c.warn("drive health %s: %v", name, err)
			return nil
		}
		return h
	}
	if transport == "mmc" || transport == "virtio" {
		return nil // no SMART
	}
	h, err := smartctlHealth("/dev/" + name)
	if err != nil {
		c.warn("drive health %s: %v", name, err)
		return nil
	}
	return h
}

var nvmeCtrl = regexp.MustCompile(`^nvme\d+`)

// nvmePassthruCmd mirrors struct nvme_passthru_cmd from <linux/nvme_ioctl.h>.
type nvmePassthruCmd struct {
	Opcode      uint8
	Flags       uint8
	Rsvd1       uint16
	NSID        uint32
	Cdw2, Cdw3  uint32
	Metadata    uint64
	Addr        uint64
	MetadataLen uint32
	DataLen     uint32
	Cdw10       uint32
	Cdw11       uint32
	Cdw12       uint32
	Cdw13       uint32
	Cdw14       uint32
	Cdw15       uint32
	TimeoutMS   uint32
	Result      uint32
}

// _IOWR('N', 0x41, struct nvme_admin_cmd), size 72.
const nvmeIoctlAdminCmd = 0xC0484E41

func nvmeHealth(dev string) (*report.DiskHealth, error) {
	f, err := os.Open(dev)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	log := make([]byte, 512)
	cmd := nvmePassthruCmd{
		Opcode:    0x02, // Get Log Page
		NSID:      0xFFFFFFFF,
		Addr:      uint64(uintptr(unsafe.Pointer(&log[0]))), //nolint:gosec // G103: the kernel ABI takes a buffer address
		DataLen:   uint32(len(log)),
		Cdw10:     uint32(len(log)/4-1)<<16 | 0x02, // NUMDL, LID 2 = SMART / Health
		TimeoutMS: 5000,
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), nvmeIoctlAdminCmd, uintptr(unsafe.Pointer(&cmd))) //nolint:gosec // G103: ioctl argument
	runtime.KeepAlive(log)
	if errno != 0 {
		return nil, fmt.Errorf("NVMe get-log-page: %w", errno)
	}
	return parseNVMeSMART(log), nil
}

// parseNVMeSMART decodes the SMART / Health Information log page (NVMe base
// spec, Log Page 02h).
func parseNVMeSMART(b []byte) *report.DiskHealth {
	u128 := func(off int) uint64 {
		// 128-bit little-endian counters; clamp to uint64.
		v := new(big.Int)
		for i := off + 15; i >= off; i-- {
			v.Lsh(v, 8).Or(v, big.NewInt(int64(b[i])))
		}
		if !v.IsUint64() {
			return ^uint64(0)
		}
		return v.Uint64()
	}
	passed := b[0] == 0 // critical warning bits all clear
	spare := int(b[3])
	used := int(b[5])
	// Data units are thousands of 512-byte units.
	read := u128(32) * 512000
	written := u128(48) * 512000
	cycles := u128(112)
	hours := u128(128)
	unsafeShutdowns := u128(144)
	media := u128(160)
	h := &report.DiskHealth{
		Source:           "nvme",
		Passed:           &passed,
		AvailableSpare:   &spare,
		PercentageUsed:   &used,
		DataReadBytes:    &read,
		DataWrittenBytes: &written,
		PowerCycles:      &cycles,
		PowerOnHours:     &hours,
		UnsafeShutdowns:  &unsafeShutdowns,
		MediaErrors:      &media,
	}
	if k := binary.LittleEndian.Uint16(b[1:3]); k != 0 { // Kelvin; 0 = not reported
		t := float64(k) - 273
		h.TemperatureC = &t
	}
	return h
}

func smartctlHealth(dev string) (*report.DiskHealth, error) {
	bin, err := exec.LookPath("smartctl")
	if err != nil {
		return nil, fmt.Errorf("smartctl not installed (needed for SATA/USB drives)")
	}
	// smartctl's exit status is a bitmask that is non-zero for many
	// non-fatal conditions, so judge success by whether the JSON parses.
	out, _ := exec.Command(bin, "--json=c", "-H", "-A", "-i", dev).Output()
	var s struct {
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature *struct {
			Current float64 `json:"current"`
		} `json:"temperature"`
		PowerOnTime *struct {
			Hours uint64 `json:"hours"`
		} `json:"power_on_time"`
		PowerCycleCount *uint64 `json:"power_cycle_count"`
		ATA             *struct {
			Table []struct {
				ID  int `json:"id"`
				Raw struct {
					Value uint64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		Smartctl struct {
			Messages []struct {
				String string `json:"string"`
			} `json:"messages"`
		} `json:"smartctl"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, fmt.Errorf("smartctl: unreadable output")
	}
	if s.SmartStatus == nil && s.Temperature == nil && s.PowerOnTime == nil {
		if len(s.Smartctl.Messages) > 0 {
			return nil, fmt.Errorf("smartctl: %s", s.Smartctl.Messages[0].String)
		}
		return nil, fmt.Errorf("smartctl: no SMART data")
	}
	h := &report.DiskHealth{Source: "smartctl", PowerCycles: s.PowerCycleCount}
	if s.SmartStatus != nil {
		h.Passed = &s.SmartStatus.Passed
	}
	if s.Temperature != nil {
		h.TemperatureC = &s.Temperature.Current
	}
	if s.PowerOnTime != nil {
		h.PowerOnHours = &s.PowerOnTime.Hours
	}
	if s.ATA != nil {
		for _, a := range s.ATA.Table {
			if a.ID == 5 { // Reallocated Sectors Count
				v := a.Raw.Value
				h.ReallocatedSectors = &v
			}
		}
	}
	return h, nil
}
