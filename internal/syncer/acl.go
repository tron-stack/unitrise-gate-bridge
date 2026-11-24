package syncer

// ⚠ THE SYSTEM ACCOUNT MUST NOT RUN A FILE ANY USER CAN CHANGE (2026-11-20).
// With nobody signed in, the Windows service runs the consume file itself, as
// LocalSystem, in session 0. The vendor's folder (C:\PTI) usually inherits
// "Authenticated Users: Modify" from C:\, so any account on the gate PC could
// edit Ptisend.bat, or drop a DLL beside Ptisend.exe, and have it run as
// SYSTEM on the next sync. So before the agent runs the import file with
// rights ordinary users don't have, it reads who may change the file and its
// folder, and refuses when a broad, non-administrator group can.
//
// The decision is here, pure and portable, so it is tested everywhere; the
// Windows half (exec_windows.go) only reads the permissions into objectACL.
//
// What counts as "could change what runs":
//   - on the FILE: write or append data, delete it, or change its permissions
//     or owner;
//   - on the FOLDER: add a file (a planted DLL, or a replacement once the old
//     one is renamed), add a subfolder, delete a file in it, delete or rename
//     the folder, or change its permissions or owner;
//   - a missing permission list ("NULL DACL", which grants everyone
//     everything, and what a FAT drive reports);
//   - the object being OWNED by one of those groups (an owner can always
//     rewrite the permissions).
// Granted to: Everyone, Authenticated Users, Users, INTERACTIVE and the other
// groups every signed-in person belongs to (broadSIDs). A permission granted
// to one named account is not judged here: telling whether that account is an
// administrator needs a lookup of its group membership.

import (
	"fmt"
	"strings"

	"github.com/mytruckyards/unitrise-gate-bridge/internal/acl"
)

// The permission model lives in internal/acl (shared with the config
// folder's own check); these names keep this file's decision readable.
type (
	aceInfo   = acl.ACE
	objectACL = acl.Object
)

// whoCanChange answers which broad group can change the object, or "".
func whoCanChange(o objectACL) string { return acl.WhoCanChange(o) }

// consumeRunRefusal decides whether the import file may run with the rights
// `as` names ("the system account" or "Administrator rights"). "" = it may.
func consumeRunRefusal(file, folder objectACL, as string) string {
	asWords := "as the system account"
	if as == "admin" {
		asWords = "with Administrator rights"
	}
	name := file.Path
	if i := strings.LastIndexAny(name, `\/`); i >= 0 { // a Windows path, whatever builds the test
		name = name[i+1:]
	}
	if folder.IsLink {
		return fmt.Sprintf("The import file's folder (%s) is a link to another place, so the Bridge won't run %s %s. Use the real folder as the save folder.", folder.Path, name, asWords)
	}
	if who := whoCanChange(folder); who != "" {
		return fmt.Sprintf("The import file's folder (%s) can be changed by any user on this PC (%s has write access), so the Bridge won't run %s %s. Limit the folder to Administrators.", folder.Path, who, name, asWords)
	}
	if who := whoCanChange(file); who != "" {
		return fmt.Sprintf("The import file %s can be changed by any user on this PC (%s has write access), so the Bridge won't run it %s. Limit the file to Administrators.", file.Path, who, asWords)
	}
	return ""
}

// consumeACLRefusal is the check consume() runs before starting the file as
// this process; per-OS (exec_windows.go reads the permissions, elsewhere it
// is a no-op). A variable so a test can stand in for Windows.
var consumeACLRefusal = platformConsumeACLRefusal
