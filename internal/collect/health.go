package collect

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jiegui2025/hwspec/internal/report"
	"github.com/jiegui2025/hwspec/internal/trust"
)

// diskHealths reads the health of the disks at these indexes in
// r.Storage, all at once: a check can take its whole timeout (an HDD
// spinning up, a hung USB bridge), and one disk's mustn't wait for
// another's. Each writes only its own disk; the warnings follow, in disk
// order.
func (c *collector) diskHealths(disks []int) {
	warnings := make([]error, len(disks))
	slots := make(chan struct{}, maxHealthChecks)
	var wg sync.WaitGroup
	for k, i := range disks {
		d := &c.r.Storage[i]
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			d.Health, warnings[k] = diskHealth(d.Name, d.Transport)
		})
	}
	wg.Wait()
	for k, err := range warnings {
		if err != nil {
			c.warn("drive health %s: %v", c.r.Storage[disks[k]].Name, err)
		}
	}
}

// maxHealthChecks is how many disks are checked at once: a server with
// dozens of disks doesn't start dozens of smartctl at once.
const maxHealthChecks = 8

// diskHealth reads SMART data (root only). NVMe drives are queried directly
// with the kernel's admin-command ioctl; other drives use smartctl when it's
// installed.
func diskHealth(name, transport string) (*report.Health, error) {
	if transport == "nvme" {
		// The command's own timeout doesn't bound a controller reset.
		health, dev := nvmeHealthFn, "/dev/"+nvmeCtrl.FindString(name)
		return within(2*nvmeTimeout, func() (*report.Health, error) { return health(dev) })
	}
	if transport == "mmc" || transport == "virtio" || transport == "xen" {
		return nil, nil // no SMART
	}
	return smartctlHealth("/dev/" + name)
}

var nvmeCtrl = regexp.MustCompile(`^nvme\d+`)

// nvmeTimeout is the health command's timeout; tests shorten it.
var nvmeTimeout = 5 * time.Second

// nvmeHealthFn is a seam: the real ioctl needs root and an NVMe drive.
var nvmeHealthFn = nvmeHealth

// runCommand runs a program with a time limit (a hung USB bridge must not
// hang the capture): killed at the timeout, its output closed commandGrace
// later, and given up on after another commandGrace. Tests replace it.
var runCommand = func(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = commandGrace // output a child of it holds open
	// Wait returns only once the program exits, and one in uninterruptible
	// sleep doesn't exit when killed.
	return within(timeout+2*commandGrace, cmd.Output)
}

func isExecutable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

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

func nvmeHealth(dev string) (*report.Health, error) {
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
		TimeoutMS: uint32(nvmeTimeout / time.Millisecond),
	}
	status, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), nvmeIoctlAdminCmd, uintptr(unsafe.Pointer(&cmd))) //nolint:gosec // G103: ioctl argument
	runtime.KeepAlive(log)
	if errno != 0 {
		return nil, fmt.Errorf("NVMe get-log-page: %w", errno)
	}
	// A positive return is the drive's NVMe status: the command was
	// delivered but failed, and the buffer holds no log page.
	if status != 0 {
		return nil, fmt.Errorf("NVMe get-log-page: drive returned status 0x%x", status)
	}
	return parseNVMeSMART(log), nil
}

// nvmeCriticalWarnings are the bits of the SMART log's first byte and
// whether each means the drive is failing (else a warning).
var nvmeCriticalWarnings = []struct {
	bit     byte
	failing bool
	text    string
}{
	{0x01, false, "available spare below the drive's threshold"},
	{0x02, false, "temperature outside the drive's limits"},
	{0x04, true, "reliability degraded by media or internal errors"},
	{0x08, true, "media placed in read-only mode"},
	{0x10, false, "volatile memory backup failed"},
	{0x20, true, "persistent memory region read-only"},
}

// parseNVMeSMART decodes the SMART / Health Information log page (NVMe base
// spec, Log Page 02h). Life used is the drive's own "percentage used"
// estimate of its rated endurance (it can exceed 100).
func parseNVMeSMART(b []byte) *report.Health {
	// 128-bit little-endian counters, scaled, saturating at the uint64
	// maximum rather than wrapping (bogus drives report all-ones).
	u128 := func(off int, scale int64) float64 {
		v := new(big.Int)
		for i := off + 15; i >= off; i-- {
			v.Lsh(v, 8).Or(v, big.NewInt(int64(b[i])))
		}
		v.Mul(v, big.NewInt(scale))
		if !v.IsUint64() {
			return float64(^uint64(0))
		}
		return float64(v.Uint64())
	}
	h := &report.Health{Status: report.StatusOK, Source: report.HealthFromNVMe}
	for _, w := range nvmeCriticalWarnings {
		if b[0]&w.bit != 0 {
			h.Reasons = append(h.Reasons, w.text)
			if w.failing {
				h.Status = report.StatusFailing
			} else if h.Status == report.StatusOK {
				h.Status = report.StatusWarning
			}
		}
	}
	used := float64(b[5])
	h.LifeUsedPercent = &used
	h.LifeRemainingPercent = new(max(0, 100-used))
	if used >= 100 && h.Status == report.StatusOK {
		h.Status = report.StatusWarning
		h.Reasons = append(h.Reasons, "rated write endurance reached: plan a replacement")
	}
	metric(h, report.MetricAvailableSpare, float64(b[3]), true)
	// Data units are thousands of 512-byte units.
	metric(h, report.MetricDataReadBytes, u128(32, 512000), true)
	metric(h, report.MetricDataWrittenBytes, u128(48, 512000), true)
	metric(h, report.MetricPowerCycles, u128(112, 1), true)
	metric(h, report.MetricPowerOnHours, u128(128, 1), true)
	metric(h, report.MetricUnsafeShutdowns, u128(144, 1), true)
	metric(h, report.MetricMediaErrors, u128(160, 1), true)
	if k := binary.LittleEndian.Uint16(b[1:3]); k != 0 { // Kelvin; 0 = not reported
		metric(h, report.MetricTemperatureC, float64(k)-273, true)
	}
	return h
}

// smartctlDirs are the only places smartctl is taken from: this runs as
// root, so a smartctl earlier in a user-controlled PATH must not be used.
var smartctlDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/local/sbin", "/usr/local/bin", "/run/current-system/sw/bin"}

// smartctlTimeout bounds one drive's smartctl; tests shorten it.
var smartctlTimeout = 30 * time.Second

// findSmartctl returns a smartctl that root alone can change, or "".
// Tests replace it.
var findSmartctl = func() string {
	for _, d := range smartctlDirs {
		if path := filepath.Join(d, "smartctl"); isExecutable(path) && trust.RootOwned(path) == nil {
			return path
		}
	}
	return ""
}

func smartctlHealth(dev string) (*report.Health, error) {
	bin := findSmartctl()
	if bin == "" {
		return nil, fmt.Errorf("smartctl not installed (needed for SATA/USB drives)")
	}
	// smartctl's exit status is a bitmask that is non-zero for many
	// non-fatal conditions, so judge success by whether the JSON parses.
	// -l devstat adds the device statistics pages, where SSDs report their
	// endurance used.
	out, runErr := runCommand(smartctlTimeout, bin, "--json=c", "-H", "-A", "-i", "-l", "devstat", dev)
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
		Endurance *struct {
			CurrentPercent float64 `json:"current_percent"`
		} `json:"endurance_used"`
		DevStat *struct {
			Pages []struct {
				Table []struct {
					Name  string  `json:"name"`
					Value float64 `json:"value"`
				} `json:"table"`
			} `json:"pages"`
		} `json:"ata_device_statistics"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		// No JSON at all: a timeout, a crash, or smartctl older than 7.0
		// (which has no --json).
		if runErr != nil {
			return nil, fmt.Errorf("smartctl: %w (smartctl 7.0 or newer is needed)", runErr)
		}
		return nil, fmt.Errorf("smartctl: output is not JSON (smartctl 7.0 or newer is needed)")
	}
	if s.SmartStatus == nil && s.Temperature == nil && s.PowerOnTime == nil {
		if len(s.Smartctl.Messages) > 0 {
			return nil, fmt.Errorf("smartctl: %s", s.Smartctl.Messages[0].String)
		}
		return nil, fmt.Errorf("smartctl: no SMART data")
	}
	h := &report.Health{Status: report.StatusUnknown, Source: report.HealthFromSmartctl}
	if s.SmartStatus != nil {
		h.Status = report.StatusOK
		if !s.SmartStatus.Passed {
			h.Status = report.StatusFailing
			h.Reasons = append(h.Reasons, "the drive's SMART self-assessment failed: back up and replace it")
		}
	}
	if s.Temperature != nil {
		metric(h, report.MetricTemperatureC, s.Temperature.Current, true)
	}
	if s.PowerOnTime != nil {
		metric(h, report.MetricPowerOnHours, float64(s.PowerOnTime.Hours), true)
	}
	if s.PowerCycleCount != nil {
		metric(h, report.MetricPowerCycles, float64(*s.PowerCycleCount), true)
	}
	if s.ATA != nil {
		for _, a := range s.ATA.Table {
			switch a.ID {
			case 5: // Reallocated Sectors Count
				metric(h, report.MetricReallocatedSectors, float64(a.Raw.Value), true)
			case 197: // Current Pending Sector Count
				metric(h, report.MetricPendingSectors, float64(a.Raw.Value), true)
			}
		}
	}
	for _, m := range []struct{ name, text string }{
		{report.MetricReallocatedSectors, "sectors reallocated"},
		{report.MetricPendingSectors, "sectors pending reallocation"},
	} {
		if v := h.Metrics[m.name]; v > 0 && h.Status != report.StatusFailing {
			h.Status = report.StatusWarning
			h.Reasons = append(h.Reasons, fmt.Sprintf("%.0f %s: the surface is degrading, keep backups current", v, m.text))
		}
	}
	// The drive's own endurance indicator (SSDs).
	used := -1.0
	if s.Endurance != nil {
		used = s.Endurance.CurrentPercent
	} else if s.DevStat != nil {
		for _, p := range s.DevStat.Pages {
			for _, row := range p.Table {
				if strings.Contains(row.Name, "Percentage Used Endurance Indicator") {
					used = row.Value
				}
			}
		}
	}
	if used >= 0 {
		h.LifeUsedPercent = &used
		h.LifeRemainingPercent = new(max(0, 100-used))
	}
	return h, nil
}
