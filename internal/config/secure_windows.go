//go:build windows

package config

import (
	"golang.org/x/sys/windows"
)

// secureDir makes the config folder belong to Administrators and limits it to
// SYSTEM + Administrators, with inheritance from %ProgramData% turned off.
//
// The access secret lives in config.json. On Unix a 0600 mode protects it,
// but Windows ignores Go file modes - a file under %ProgramData% inherits
// ACLs that typically let every local user READ it, and every user may
// create a folder there. The service (LocalSystem) and the admins who run
// `pair`/`service install` are the only principals that need the folder, so
// the DACL names exactly those two, applied with inheritance so config.json
// and the rosters inherit it. The OWNER is set too (2026-11-20): an owner can
// always rewrite the permissions, so a folder some user created first must be
// taken from them, not merely re-permissioned.
func secureDir(path string) error {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	// Taking a folder that grants us nothing needs these (SYSTEM and an
	// elevated Administrator hold them, switched off by default).
	enablePrivileges("SeTakeOwnershipPrivilege", "SeRestorePrivilege")
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, admins, nil, nil, nil); err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(admins),
			},
		},
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	// PROTECTED_DACL: stop inheriting the permissive %ProgramData% ACEs.
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
}

// enablePrivileges switches on privileges this process already holds (best
// effort: one it doesn't hold simply stays off).
func enablePrivileges(names ...string) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return
	}
	defer tok.Close()
	for _, n := range names {
		var luid windows.LUID
		name, err := windows.UTF16PtrFromString(n)
		if err != nil || windows.LookupPrivilegeValue(nil, name, &luid) != nil {
			continue
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil) //nolint:errcheck - best effort
	}
}
