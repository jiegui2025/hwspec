package collect

import (
	"strings"
	"testing"

	"github.com/jiegui2025/hwspec/internal/report"
)

func blacklisted(bl []report.BlacklistedModule) string {
	var out []string
	for _, b := range bl {
		out = append(out, b.Kind+":"+b.Module+"@"+b.Source)
	}
	return strings.Join(out, " ")
}

// modprobe.d is read as kmod reads it: a file name from the first
// directory that has it, the files in name order whatever their
// directory, *.conf only; blacklist lines and no-op installs count. The
// command line gives modprobe.blacklist= and the kernel's
// module_blacklist=, and nothing else (#212).
func TestModuleBlacklist(t *testing.T) {
	file, _ := fakeRoot(t)
	file("/etc/modprobe.d/x.conf", "# local\nblacklist iwlwifi\n")
	file("/usr/lib/modprobe.d/x.conf", "blacklist masked_by_etc\n") // masked: same name in /etc
	file("/usr/lib/modprobe.d/a.conf", "blacklist snd-hda-intel\n")
	file("/run/modprobe.d/m.conf", "options x y=1\nblacklist \\\n  pcspkr\ninstall cramfs /bin/false\n")
	file("/lib/modprobe.d/z.conf", "blacklist last\n")
	file("/etc/modprobe.d/README", "blacklist not_a_conf\n")
	file("/proc/cmdline", "BOOT_IMAGE=/vmlinuz root=UUID=3f2a1b9c-0000-4c1e-9a7d-123456789abc rw modprobe.blacklist=nouveau,,pc-spkr quiet module_blacklist=amdgpu module_blacklist\n")
	col := &collector{r: &report.Report{}}
	got := blacklisted(col.moduleBlacklist())
	want := "blacklist:snd_hda_intel@/usr/lib/modprobe.d/a.conf blacklist:pcspkr@/run/modprobe.d/m.conf install:cramfs@/run/modprobe.d/m.conf " +
		"blacklist:iwlwifi@/etc/modprobe.d/x.conf blacklist:last@/lib/modprobe.d/z.conf blacklist:nouveau@cmdline blacklist:pc_spkr@cmdline kernel:amdgpu@cmdline"
	if got != want || len(col.r.Warnings) != 0 {
		t.Errorf("got  %s\nwant %s\nwarnings %q", got, want, col.r.Warnings)
	}
	if strings.Contains(got, "UUID") || strings.Contains(got, "3f2a1b9c") {
		t.Error("the command line's other values reached the capture")
	}
}

// Nothing blacklisted is an empty list, not an absent one; a file or the
// command line that can't be read is a warning.
func TestModuleBlacklistEmptyAndUnreadable(t *testing.T) {
	file, _ := fakeRoot(t)
	file("/proc/cmdline", "root=/dev/sda1\n")
	col := &collector{r: &report.Report{}}
	if bl := col.moduleBlacklist(); bl == nil || len(bl) != 0 || len(col.r.Warnings) != 0 {
		t.Errorf("%+v, warnings %q", bl, col.r.Warnings)
	}
	file, _ = fakeRoot(t)
	file("/etc/modprobe.d/bad.conf/x", "") // a directory: can't be read
	col = &collector{r: &report.Report{}}
	col.moduleBlacklist()
	w := strings.Join(col.r.Warnings, "\n")
	if !strings.Contains(w, "modprobe.d /etc/modprobe.d/bad.conf: ") || !strings.Contains(w, "kernel command line (module blacklist): ") {
		t.Errorf("warnings %q", w)
	}
}

func TestParseBlacklist(t *testing.T) {
	for text, want := range map[string]string{
		"blacklist a\nblacklist b-c\n":             "blacklist:a blacklist:b_c",
		"# blacklist commented\n  blacklist d  \n": "blacklist:d",
		"blacklist\nblacklisted e\noptions f g\n":  "",
		"blacklist \\\nh\nblacklist i":             "blacklist:h blacklist:i",
		"blacklist j \\":                           "blacklist:j",
		"\r\nblacklist k\r\n":                      "blacklist:k",
		"blacklist \\\r\nm\r\n":                    "blacklist:m",
		"#blacklist n\n":                           "",
		"   # blacklist indented comment\n":        "",
		"install usb-storage /bin/true\ninstall cramfs /usr/bin/false\ninstall x false\ninstall y true\ninstall z /bin/false\n": "install:usb_storage install:cramfs install:x install:y install:z",
		// Commands that do something, or aren't a whole no-op, load the
		// module their own way: not counted.
		"install fred /sbin/modprobe barney; /sbin/modprobe --ignore-install fred\ninstall g /bin/false extra\ninstall h\ninstall k /sbin/load-k\n": "",
	} {
		var got []string
		for _, e := range parseBlacklist(text) {
			got = append(got, e.Kind+":"+e.Module)
		}
		if got := strings.Join(got, " "); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}
