//go:build windows

package service

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// windowsServiceAccount reads the service's registry entry (readable without
// administrator rights, unlike opening the SCM for configuration): whether it
// exists and the account it runs as (ObjectName).
func windowsServiceAccount(name string) (bool, string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+name, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	defer k.Close()
	account, _, err := k.GetStringValue("ObjectName")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return true, "", err
	}
	return true, account, nil
}
