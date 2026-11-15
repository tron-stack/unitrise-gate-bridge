package syncer

// ⚠ THE FILE NAME AND THE CONSUME COMMAND COME FROM THE SERVER, AND THIS
// PROGRAM RUNS WITH THE SITE PC'S OWN RIGHTS (2026-11-13, part 6 audit ED-6,
// CRITICAL). The consume value used to be free text, joined to the save folder
// and handed to `cmd /S /C` (or `/bin/sh -c`), so `Ptisend.bat & powershell
// ...` typed into the property's Gate card ran as the service account (SYSTEM)
// on the computer that holds the gate software; and the file name was joined
// to the save folder with no check, so `..\..\Windows\...` wrote outside it. A
// phished or departed manager, a support session or a compromised backend
// reached every site that way at once.
//
// Both are now held to ONE rule before anything is written or started, the
// same rule the server refuses at Save and withholds from a stored value
// (truckpark-backend util/gateBridge.js bridgeFileProblem, the contract's
// "settings the agent acts on"):
//
//   - a PLAIN FILE NAME: 1 to 100 characters, starting with a letter or a
//     digit, using only letters, digits, spaces, dots, dashes and underscores -
//     so no folder, no "..", no drive, no quote and no shell character;
//   - never a name Windows reserves (CON, PRN, AUX, NUL, COM1, LPT1 ...) and
//     never ending with a dot or a space (Windows drops them, so
//     "Ptisend.bat." would be Ptisend.bat);
//   - the consume command is a .bat, .cmd or .exe file, and it is run only
//     when it is a real file INSIDE the save folder (not a link to one);
//   - the gate file is never a program or a script: it is data the gate
//     software reads, and a written .bat that the consume command then ran
//     would be the same hole again.
//
// The consume file is started DIRECTLY, never through a shell: on Windows the
// program the agent starts is the file itself, by its full path, with no
// arguments (a .bat or .cmd runs inside cmd.exe because that is how Windows
// runs a batch file, but cmd.exe is handed only the quoted path); elsewhere it
// is exec'd. The one exception is the Windows service reaching the signed-in
// user's session (exec_windows.go runConsumeInSession), which needs cmd.exe to
// send the output to a file because Windows does not let a service hand its
// file handles to a process in another session: that line is built from the
// checked path and a random output file only (sessionConsumeLine below),
// never from text the server sent.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxPlainName = 100

var plainNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*$`)

// Windows reserves these as device names, with or without an extension
// ("NUL.bat" is the NUL device, not a file).
var reservedDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)

var consumeExts = map[string]bool{".bat": true, ".cmd": true, ".exe": true}

// What a gate file must never be named: anything Windows, or a vendor's own
// batch file, could run or open as code.
var runnableExts = map[string]bool{
	".bat": true, ".cmd": true, ".exe": true, ".com": true, ".scr": true, ".pif": true,
	".msi": true, ".msp": true, ".mst": true, ".ps1": true, ".psm1": true, ".psd1": true,
	".ps1xml": true, ".psc1": true, ".vb": true, ".vbs": true, ".vbe": true, ".js": true,
	".jse": true, ".wsf": true, ".wsh": true, ".wsc": true, ".ws": true, ".sct": true,
	".hta": true, ".cpl": true, ".msc": true, ".dll": true, ".ocx": true, ".sys": true,
	".drv": true, ".lnk": true, ".url": true, ".reg": true, ".inf": true, ".jar": true,
	".application": true, ".appref-ms": true, ".gadget": true, ".scf": true, ".chm": true,
	".hlp": true, ".shb": true, ".shs": true, ".appx": true, ".msix": true,
	".appxbundle": true, ".msixbundle": true, ".settingcontent-ms": true, ".library-ms": true,
}

// plainNameProblem answers why a name is not a plain file name, or "".
func plainNameProblem(name string) string {
	switch {
	case name == "":
		return "it is blank"
	case len(name) > maxPlainName:
		return fmt.Sprintf("it is longer than %d characters", maxPlainName)
	case strings.ContainsAny(name, `/\`):
		return "it names a folder, and only a file in the save folder is allowed"
	case strings.Contains(name, ".."):
		return `it contains "..", which can point outside the save folder`
	case !plainNameRE.MatchString(name):
		return "it uses characters other than letters, digits, spaces, dots, dashes and underscores"
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return "it ends with a dot or a space, which Windows drops"
	}
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reservedDevice.MatchString(strings.TrimRight(base, " ")) {
		return "it is a name Windows reserves for a device"
	}
	return ""
}

// consumeNameProblem: a plain .bat, .cmd or .exe file name, or why not.
func consumeNameProblem(name string) string {
	if p := plainNameProblem(name); p != "" {
		return p
	}
	if !consumeExts[strings.ToLower(filepath.Ext(name))] {
		return "it isn't a .bat, .cmd or .exe file"
	}
	return ""
}

// fileNameProblem: a plain file name that is not a program or a script, or
// why not.
func fileNameProblem(name string) string {
	if p := plainNameProblem(name); p != "" {
		return p
	}
	if runnableExts[strings.ToLower(filepath.Ext(name))] {
		return "it is a program or script file, and the gate file is data the gate software reads"
	}
	return ""
}

// insideSavePath joins a checked plain name to the save folder and confirms
// the result is a file directly in that folder.
func insideSavePath(savePath, name string) (string, error) {
	dir := filepath.Clean(savePath)
	full := filepath.Join(dir, name)
	if filepath.Dir(full) != dir || filepath.Base(full) != name {
		return "", errors.New("it would leave the save folder")
	}
	return full, nil
}

// whereFrom names the setting a refusal is about, so the person reading the
// agent's status knows where to fix it.
func whereFrom(local bool) string {
	if local {
		return "this PC's config.json"
	}
	return "the property's Gate card in UnitRise"
}

// gateFilePath resolves the gate file the agent writes, or refuses it in
// words. Nothing is written when it refuses.
func gateFilePath(savePath, name string, local bool) (string, error) {
	if p := fileNameProblem(name); p != "" {
		return "", fmt.Errorf("the gate file name %q was refused (%s), so nothing was written - fix it on %s", name, p, whereFrom(local))
	}
	full, err := insideSavePath(savePath, name)
	if err != nil {
		return "", fmt.Errorf("the gate file name %q was refused (%s), so nothing was written - fix it on %s", name, err, whereFrom(local))
	}
	return full, nil
}

// resolveConsume resolves the consume file the agent runs, or refuses it in
// words. Nothing is run when it refuses.
func resolveConsume(savePath, name string, local bool) (string, error) {
	if p := consumeNameProblem(name); p != "" {
		return "", fmt.Errorf("the consume command %q was refused (%s), so it was not run - set it to the plain name of the .bat, .cmd or .exe file in the save folder, like Ptisend.bat, on %s", name, p, whereFrom(local))
	}
	full, err := insideSavePath(savePath, name)
	if err != nil {
		return "", fmt.Errorf("the consume command %q was refused (%s), so it was not run - fix it on %s", name, err, whereFrom(local))
	}
	fi, err := os.Lstat(full)
	switch {
	case err != nil && os.IsNotExist(err):
		return "", fmt.Errorf("the consume command %q isn't in the save folder (%s), so it was not run - check the file's exact name there", name, filepath.Clean(savePath))
	case err != nil:
		return "", fmt.Errorf("the consume command %q couldn't be checked (%v), so it was not run", name, err)
	case fi.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("the consume command %q is a link, not the file itself, so it was not run - put the real file in the save folder", name)
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("the consume command %q isn't a file, so it was not run", name)
	}
	return full, nil
}

// sessionConsumeLine is the one command line the Windows service hands
// cmd.exe to run the consume file in the signed-in user's session with its
// output going to a file (see the note at the top). Every part is quoted, and
// none of them can hold a quote: the consume path passed resolveConsume, the
// output path is the save folder plus a random hex name, and a Windows path
// never contains `"`. `/D` keeps cmd.exe from running any AutoRun command set
// in the registry first.
func sessionConsumeLine(cmdExe, full, outPath string) (string, error) {
	for _, p := range []string{cmdExe, full, outPath} {
		if p == "" || strings.ContainsAny(p, "\"\r\n") {
			return "", errors.New("a path in the consume command line can't be quoted safely")
		}
	}
	return `"` + cmdExe + `" /D /S /C ""` + full + `" > "` + outPath + `" 2>&1"`, nil
}
