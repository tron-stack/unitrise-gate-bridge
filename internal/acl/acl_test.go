package acl

import (
	"strings"
	"testing"
)

const (
	sidUsers    = "S-1-5-32-545"
	sidAuthUser = "S-1-5-11"
	sidCreator  = "S-1-3-0"
	sidSomeone  = "S-1-5-21-1004336348-1177238915-682003330-1001"

	maskFull        = 0x001F01FF
	maskModify      = 0x001301BF
	maskReadExecute = 0x001200A9
)

// What secureDir leaves: owned by Administrators, SYSTEM and Administrators
// full control, nothing inherited.
func agentFolder() Object {
	return Object{Path: `C:\ProgramData\UnitRiseGateBridge`, Owner: SIDAdministrators, ACEs: []ACE{
		{SID: SIDSystem, Allow: true, Mask: maskFull},
		{SID: SIDAdministrators, Allow: true, Mask: maskFull},
	}}
}

func TestTheAgentsOwnFolderPasses(t *testing.T) {
	if p := PrivateProblem(agentFolder(), "The config folder"); p != "" {
		t.Fatalf("SYSTEM + Administrators only must pass: %q", p)
	}
	f := agentFolder()
	f.Owner = SIDSystem // a folder the service created
	f.ACEs = append(f.ACEs, ACE{SID: sidCreator, Allow: true, Mask: maskFull, InheritOnly: true})
	if p := PrivateProblem(f, "The config folder"); p != "" {
		t.Fatalf("owned by SYSTEM, with an inherit-only CREATOR OWNER entry, must pass: %q", p)
	}
	// Reading is not changing: a read entry for Users is no reason to refuse.
	f.ACEs = append(f.ACEs, ACE{SID: sidUsers, Allow: true, Mask: maskReadExecute})
	if p := PrivateProblem(f, "The config folder"); p != "" {
		t.Fatalf("a read-only entry is not a change right: %q", p)
	}
}

// The planting cases: a folder a user created first, one %ProgramData% left
// open, a link, a missing permission list.
func TestAnythingOthersCouldHaveWrittenIsRefused(t *testing.T) {
	cases := map[string]func() Object{
		"owned by the user who created it": func() Object {
			o := agentFolder()
			o.Owner, o.OwnerName = sidSomeone, `PC\dana`
			return o
		},
		"Users may add files (what %ProgramData% hands down)": func() Object {
			o := agentFolder()
			o.ACEs = append(o.ACEs, ACE{SID: sidUsers, Allow: true, Mask: FileWriteData | FileAppendData})
			return o
		},
		"one named account may modify it": func() Object {
			o := agentFolder()
			o.ACEs = append(o.ACEs, ACE{SID: sidSomeone, Name: `PC\dana`, Allow: true, Mask: maskModify})
			return o
		},
		"a link to another place": func() Object {
			o := agentFolder()
			o.IsLink = true
			return o
		},
		"no permission list at all (even owned by Administrators)": func() Object {
			return Object{Path: `C:\ProgramData\UnitRiseGateBridge`, Owner: SIDAdministrators, NullDACL: true}
		},
		"no owner recorded": func() Object { o := agentFolder(); o.Owner = ""; return o },
	}
	for name, mk := range cases {
		if p := PrivateProblem(mk(), "The config folder"); p == "" {
			t.Errorf("%s: trusted", name)
		}
	}
	o := agentFolder()
	o.Owner, o.OwnerName = sidSomeone, `PC\dana`
	if p := PrivateProblem(o, "The config folder"); !strings.Contains(p, `owned by PC\dana, not by SYSTEM or Administrators`) {
		t.Fatalf("the sentence names the owner: %q", p)
	}
}

func TestADenyAheadOfTheGrantIsRead(t *testing.T) {
	o := agentFolder()
	o.ACEs = append([]ACE{{SID: sidAuthUser, Allow: false, Mask: maskModify}}, append(o.ACEs, ACE{SID: sidAuthUser, Allow: true, Mask: maskModify})...)
	if p := PrivateProblem(o, "The config folder"); p != "" {
		t.Fatalf("a deny ahead of the grant takes the rights away: %q", p)
	}
	if who := WhoCanChange(o); who != "" {
		t.Fatalf("WhoCanChange ignores a cancelled grant too: %q", who)
	}
}
