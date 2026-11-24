// Package acl is the agent's reading of Windows file permissions, kept pure
// and portable so every decision is tested on every platform; read_windows.go
// is the only part that touches the OS.
//
// Two judgements, for two kinds of folder:
//
//   - WhoCanChange is for a folder the VENDOR controls (C:\PTI, where the
//     import file lives): it names a broad, non-administrator group that can
//     change the object, because a vendor folder legitimately carries other
//     named accounts the agent can't judge.
//   - PrivateProblem is for a folder the AGENT controls (its config folder,
//     %ProgramData%\UnitRiseGateBridge, which it sets to SYSTEM and
//     Administrators itself): an allow-list. Anything owned by, or changeable
//     by, anyone but SYSTEM and Administrators is a problem, and so is a link.
package acl

import "strings"

// ACE is one entry of a permission list, reduced to what the decisions need.
type ACE struct {
	SID         string // string form, e.g. S-1-5-11
	Name        string // the account's name when the reader could look it up
	Allow       bool   // false = a deny entry
	Mask        uint32
	InheritOnly bool // applies only to things created inside, not this object
}

// Object is who may do what to one file or folder.
type Object struct {
	Path      string
	IsLink    bool // a symbolic link, a junction or another reparse point
	NullDACL  bool // no permission list at all: everyone has full control
	Owner     string
	OwnerName string
	ACEs      []ACE
}

// Access bits that let a holder change a file or what a folder holds (winnt.h).
const (
	FileWriteData   = 0x00000002 // file: write data; folder: add a file
	FileAppendData  = 0x00000004 // file: append; folder: add a subfolder
	FileDeleteChild = 0x00000040 // folder: delete a file inside
	Delete          = 0x00010000
	WriteDAC        = 0x00040000
	WriteOwner      = 0x00080000
	GenericAll      = 0x10000000
	GenericWrite    = 0x40000000

	ChangeMask = FileWriteData | FileAppendData | FileDeleteChild | Delete |
		WriteDAC | WriteOwner | GenericAll | GenericWrite
)

// The two principals the agent's own folder may belong to.
const (
	SIDSystem         = "S-1-5-18"
	SIDAdministrators = "S-1-5-32-544"
	SIDEveryone       = "S-1-1-0"
)

// broadSIDs: groups every ordinary signed-in person is in, by SID (so a
// German or Spanish Windows, where "Users" is "Benutzer" or "Usuarios", reads
// the same).
var broadSIDs = map[string]string{
	SIDEveryone:    "Everyone",
	"S-1-5-11":     "Authenticated Users",
	"S-1-5-32-545": "Users",
	"S-1-5-4":      "INTERACTIVE",
	"S-1-5-32-546": "Guests",
	"S-1-5-7":      "ANONYMOUS LOGON",
	"S-1-2-0":      "LOCAL",
	"S-1-2-1":      "CONSOLE LOGON",
	"S-1-5-14":     "REMOTE INTERACTIVE LOGON",
	"S-1-5-32-555": "Remote Desktop Users",
	"S-1-5-2":      "NETWORK",
}

// BroadName names a broad group by SID, or "" (the domain's Users and Guests
// groups carry the domain's own prefix and end in -513 / -514).
func BroadName(sid string) string {
	if n, ok := broadSIDs[sid]; ok {
		return n
	}
	if strings.HasPrefix(sid, "S-1-5-21-") {
		switch {
		case strings.HasSuffix(sid, "-513"):
			return "Domain Users"
		case strings.HasSuffix(sid, "-514"):
			return "Domain Guests"
		}
	}
	return ""
}

func trusted(sid string) bool { return sid == SIDSystem || sid == SIDAdministrators }

// display names a principal for a sentence: its looked-up name, else a known
// group's name, else the SID itself.
func display(sid, name string) string {
	switch {
	case name != "":
		return name
	case sid == SIDSystem:
		return "SYSTEM"
	case sid == SIDAdministrators:
		return "Administrators"
	case BroadName(sid) != "":
		return BroadName(sid)
	case sid == "":
		return "nobody (no owner is recorded)"
	}
	return sid
}

// changers walks the entries in order and calls fn for every principal an
// allow entry lets change the object (after earlier deny entries for the same
// SID, or for Everyone, took rights away). fn returning true stops the walk.
func changers(o Object, fn func(ACE) bool) {
	denied := map[string]uint32{}
	for _, a := range o.ACEs {
		if a.InheritOnly {
			continue
		}
		if !a.Allow {
			denied[a.SID] |= a.Mask
			continue
		}
		if a.Mask&^denied[a.SID]&^denied[SIDEveryone]&ChangeMask != 0 {
			if fn(a) {
				return
			}
		}
	}
}

// WhoCanChange answers which BROAD group can change the object, or "".
func WhoCanChange(o Object) string {
	if o.NullDACL {
		return "Everyone (no permissions are set on it)"
	}
	if n := BroadName(o.Owner); n != "" {
		return n + " (it owns it)"
	}
	who := ""
	changers(o, func(a ACE) bool {
		if n := BroadName(a.SID); n != "" {
			who = n
			return true
		}
		return false
	})
	return who
}

// PrivateProblem judges an object in the agent's OWN folder: only SYSTEM and
// Administrators may own it or change it, and it may not be a link. what
// names it in the sentence ("The config folder", "config.json"). "" = fine.
func PrivateProblem(o Object, what string) string {
	switch {
	case o.IsLink:
		return what + " (" + o.Path + ") is a link to another place, not a real folder or file"
	case o.NullDACL:
		return what + " (" + o.Path + ") has no permissions set, so anyone on this PC could have written it"
	case !trusted(o.Owner):
		return what + " (" + o.Path + ") is owned by " + display(o.Owner, o.OwnerName) + ", not by SYSTEM or Administrators"
	}
	problem := ""
	changers(o, func(a ACE) bool {
		if !trusted(a.SID) {
			problem = what + " (" + o.Path + ") can be changed by " + display(a.SID, a.Name) + ", not only by SYSTEM and Administrators"
			return true
		}
		return false
	})
	return problem
}
