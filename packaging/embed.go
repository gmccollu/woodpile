// Package packaging holds the service definitions the installer packages ship, so
// that `woodpile install` (for machines without a package) installs exactly the
// same files: the systemd unit and sysctl drop-in (deb/rpm), the launchd plist,
// newsyslog config, service-account script and launcher app (pkg). Edit the files, not copies.
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

	// MacAppInfoPlist, MacAppLauncher and MacAppIcon are /Applications/Woodpile.app
	// (packaging/macos/app, packaging/icons): a launcher that opens the web UI. The plist's
	// version placeholder is filled in at install.
	//go:embed macos/app/Info.plist
	MacAppInfoPlist string
	//go:embed macos/app/Woodpile
	MacAppLauncher string
	//go:embed icons/woodpile.icns
	MacAppIcon string
)

// The Windows data-folder ACL (the MSI's PermissionEx SDDL) lives in internal/winacl.
