package collect

import "sync"

// captureMu serialises captures: Collect, CollectRecorded and Paths, and
// the latter two replace the hooks below while they run.
var captureMu sync.Mutex

// saveHooks records every replaceable hook (the root, the calls that aren't
// file reads, denied paths, tracing) and returns a function that restores
// them all, so nothing a test or a recording changed leaks into the next
// capture.
func saveHooks() (restore func()) {
	r, eu, hn, un := root, geteuid, hostname, uname
	et, bt, om, nv := ethtoolDrvinfo, readBTInfo, openMgmt, nvmeHealthFn
	fs, rc, dn, tr := findSmartctl, runCommand, unreadable, traceRead
	tt, bv, oh := tpmTransmit, readBTVersion, openHCI
	return func() {
		root, geteuid, hostname, uname = r, eu, hn, un
		ethtoolDrvinfo, readBTInfo, openMgmt, nvmeHealthFn = et, bt, om, nv
		findSmartctl, runCommand, unreadable, traceRead = fs, rc, dn, tr
		tpmTransmit, readBTVersion, openHCI = tt, bv, oh
	}
}
