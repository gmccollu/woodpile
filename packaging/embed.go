// Package packaging holds the service definitions the installer packages ship, so
// that `woodpile install` (for machines without a package) installs exactly the
// same files: the systemd unit and sysctl drop-in (deb/rpm), the launchd plist,
// newsyslog config and service-account script (pkg). Edit the files, not copies.
package packaging

import _ "embed"

var (
	// SystemdUnit is packaging/linux/woodpile.service.
	//go:embed linux/woodpile.service
	SystemdUnit string

	// SysctlDropIn is packaging/linux/60-woodpile.conf.
	//go:embed linux/60-woodpile.conf
	SysctlDropIn string

	// LaunchdPlist is packaging/macos/com.woodpile.daemon.plist.
	//go:embed macos/com.woodpile.daemon.plist
	LaunchdPlist string

	// NewsyslogConf is packaging/macos/com.woodpile.daemon.newsyslog.conf.
	//go:embed macos/com.woodpile.daemon.newsyslog.conf
	NewsyslogConf string

	// MacAccountScript is packaging/macos/scripts/common.sh, whose
	// ensure_service_account creates the _woodpile user and group.
	//go:embed macos/scripts/common.sh
	MacAccountScript string
)

// Windows data-folder ACL, the same SDDL as the MSI's PermissionEx in
// windows/woodpile.wxs: owner Administrators, a protected DACL with full control for
// SYSTEM and Administrators and modify (no permission changes) for the service's
// virtual account, NT SERVICE\Woodpile, whose SID is S-1-5-80-<SHA-1 of "WOODPILE">.
const (
	WindowsServiceSID = "S-1-5-80-276366627-2944175572-1807786847-528126453-1705305312"
	WindowsDataSDDL   = "O:BAG:SYD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1301bf;;;" + WindowsServiceSID + ")"
)
