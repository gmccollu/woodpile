//go:build windows

package service

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// windowsServiceState reads the registry (readable without administrator rights,
// unlike opening the SCM for configuration): whether the service's entry exists,
// and whether the MSI's key HKLM\<msiKey> (Software\Woodshed, or the old
// Software\Woodpile) does. Only the MSI writes that key (its Start menu
// component), so it marks a package install.
func windowsServiceState(name, msiKey string) (exists, msi bool, err error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+name, registry.QUERY_VALUE)
	switch {
	case err == nil:
		exists = true
		k.Close()
	case !errors.Is(err, registry.ErrNotExist):
		return false, false, err
	}
	k, err = registry.OpenKey(registry.LOCAL_MACHINE, msiKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	switch {
	case err == nil:
		msi = true
		k.Close()
	case !errors.Is(err, registry.ErrNotExist):
		return exists, false, err
	}
	return exists, msi, nil
}
