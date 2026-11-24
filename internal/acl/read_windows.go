//go:build windows

package acl

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Read reads a file's or folder's owner and permission list, and whether it
// is a link (a symbolic link, a junction or any other reparse point - read
// without following it).
func Read(path string) (Object, error) {
	o := Object{Path: path}
	fi, err := os.Lstat(path)
	if err != nil {
		return o, err
	}
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		o.IsLink = true
	}
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok && d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		o.IsLink = true
	}
	if o.IsLink {
		return o, nil // never read (or trust) the permissions of what a link points at
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return o, err
	}
	if owner, _, err := sd.Owner(); err == nil && owner != nil {
		o.Owner = owner.String()
		o.OwnerName = lookup(owner)
	}
	dacl, _, err := sd.DACL()
	switch {
	case err == windows.ERROR_OBJECT_NOT_FOUND || (err == nil && dacl == nil):
		o.NullDACL = true // no DACL at all (or a NULL one): everyone has full control
		return o, nil
	case err != nil:
		return o, err
	}
	const (
		allowedCallback = 9
		deniedCallback  = 10
	)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return o, err
		}
		var allow bool
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE, allowedCallback:
			allow = true
		case windows.ACCESS_DENIED_ACE_TYPE, deniedCallback:
			allow = false
		default:
			continue // object ACEs: not used on files
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		o.ACEs = append(o.ACEs, ACE{
			SID:         sid.String(),
			Name:        lookup(sid),
			Allow:       allow,
			Mask:        uint32(ace.Mask),
			InheritOnly: ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0,
		})
	}
	return o, nil
}

// lookup names an account for a sentence ("PC\dana"), best effort.
func lookup(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil || account == "" {
		return ""
	}
	if domain != "" {
		return domain + `\` + account
	}
	return account
}
