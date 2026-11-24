package syncer

import (
	"strings"
	"testing"
)

const (
	sidSystem   = "S-1-5-18"
	sidAdmins   = "S-1-5-32-544"
	sidUsers    = "S-1-5-32-545"
	sidAuthUser = "S-1-5-11"
	sidEveryone = "S-1-1-0"

	maskModify      = 0x001301BF // "Modify"
	maskReadExecute = 0x001200A9 // "Read & execute"
	maskFull        = 0x001F01FF
	maskCreateFiles = 0x00000002 // folder: "Create files / write data" only
)

func adminOnly(path string) objectACL {
	return objectACL{Path: path, Owner: sidAdmins, ACEs: []aceInfo{
		{SID: sidSystem, Allow: true, Mask: maskFull},
		{SID: sidAdmins, Allow: true, Mask: maskFull},
		{SID: sidUsers, Allow: true, Mask: maskReadExecute},
	}}
}

func TestTheUsualCPTIFolderIsRefused(t *testing.T) {
	folder := adminOnly(`C:\PTI`)
	// What C:\ hands every subfolder on a stock Windows install.
	folder.ACEs = append(folder.ACEs, aceInfo{SID: sidAuthUser, Allow: true, Mask: maskModify})
	got := consumeRunRefusal(adminOnly(`C:\PTI\Ptisend.bat`), folder, "system")
	for _, want := range []string{`The import file's folder (C:\PTI) can be changed by any user on this PC`, "Authenticated Users", "won't run Ptisend.bat as the system account", "Limit the folder to Administrators."} {
		if !strings.Contains(got, want) {
			t.Fatalf("refusal = %q, want it to contain %q", got, want)
		}
	}
}

func TestAdministratorOnlyPermissionsPass(t *testing.T) {
	if got := consumeRunRefusal(adminOnly(`C:\PTI\Ptisend.bat`), adminOnly(`C:\PTI`), "system"); got != "" {
		t.Fatalf("Users with read & execute only is fine, got %q", got)
	}
}

func TestEveryWayAnOrdinaryUserCouldChangeWhatRuns(t *testing.T) {
	cases := map[string]func() (objectACL, objectACL){
		"the file itself is writable by Users": func() (objectACL, objectACL) {
			f := adminOnly(`C:\PTI\Ptisend.bat`)
			f.ACEs = append(f.ACEs, aceInfo{SID: sidUsers, Allow: true, Mask: maskModify})
			return f, adminOnly(`C:\PTI`)
		},
		"Everyone may add a file to the folder (a planted DLL)": func() (objectACL, objectACL) {
			d := adminOnly(`C:\PTI`)
			d.ACEs = append(d.ACEs, aceInfo{SID: sidEveryone, Allow: true, Mask: maskCreateFiles})
			return adminOnly(`C:\PTI\Ptisend.exe`), d
		},
		"the folder has no permission list": func() (objectACL, objectACL) {
			return adminOnly(`C:\PTI\Ptisend.bat`), objectACL{Path: `C:\PTI`, NullDACL: true}
		},
		"the file is owned by Users": func() (objectACL, objectACL) {
			f := adminOnly(`C:\PTI\Ptisend.bat`)
			f.Owner = sidUsers
			return f, adminOnly(`C:\PTI`)
		},
		"Domain Users may modify the folder": func() (objectACL, objectACL) {
			d := adminOnly(`C:\PTI`)
			d.ACEs = append(d.ACEs, aceInfo{SID: "S-1-5-21-1004336348-1177238915-682003330-513", Allow: true, Mask: maskModify})
			return adminOnly(`C:\PTI\Ptisend.bat`), d
		},
		"INTERACTIVE may delete files in the folder": func() (objectACL, objectACL) {
			d := adminOnly(`C:\PTI`)
			d.ACEs = append(d.ACEs, aceInfo{SID: "S-1-5-4", Allow: true, Mask: 0x40})
			return adminOnly(`C:\PTI\Ptisend.bat`), d
		},
	}
	for name, mk := range cases {
		f, d := mk()
		if got := consumeRunRefusal(f, d, "system"); got == "" {
			t.Errorf("%s: not refused", name)
		}
	}
}

// An entry that applies only to things created inside the folder (CREATOR
// OWNER-style inheritance) grants nothing on the folder itself; a deny for the
// same group cancels the grant.
func TestInheritOnlyAndDenyEntriesAreRead(t *testing.T) {
	d := adminOnly(`C:\PTI`)
	d.ACEs = append(d.ACEs, aceInfo{SID: sidUsers, Allow: true, Mask: maskModify, InheritOnly: true})
	if got := consumeRunRefusal(adminOnly(`C:\PTI\x.bat`), d, "system"); got != "" {
		t.Errorf("an inherit-only entry was counted: %q", got)
	}
	d = adminOnly(`C:\PTI`)
	d.ACEs = append([]aceInfo{{SID: sidAuthUser, Allow: false, Mask: maskModify}}, append(d.ACEs, aceInfo{SID: sidAuthUser, Allow: true, Mask: maskModify})...)
	if got := consumeRunRefusal(adminOnly(`C:\PTI\x.bat`), d, "system"); got != "" {
		t.Errorf("a deny ahead of the grant was ignored: %q", got)
	}
}

func TestTheRefusalSaysWhichRightsItWouldHaveRunWith(t *testing.T) {
	d := adminOnly(`C:\PTI`)
	d.ACEs = append(d.ACEs, aceInfo{SID: sidAuthUser, Allow: true, Mask: maskModify})
	if got := consumeRunRefusal(adminOnly(`C:\PTI\x.bat`), d, "admin"); !strings.Contains(got, "with Administrator rights") {
		t.Fatalf("an elevated run is named as such: %q", got)
	}
}

// A save folder that is a link (a junction to somewhere else) is not judged
// by what it points at: it isn't run from as SYSTEM at all.
func TestAnImportFolderThatIsALinkIsRefused(t *testing.T) {
	d := adminOnly(`C:\PTI`)
	d.IsLink = true
	if got := consumeRunRefusal(adminOnly(`C:\PTI\Ptisend.bat`), d, "system"); !strings.Contains(got, "is a link to another place") {
		t.Fatalf("refusal = %q", got)
	}
}
