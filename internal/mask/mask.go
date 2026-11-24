// Package mask keeps gate codes out of everything the agent shows or sends
// that is not the gate file itself.
//
// The local dashboard has no sign-in (any account on the gate PC can open
// it), and the consume command's output (a vendor's import tool can echo the
// lines it read) goes to the dashboard, the log and the UnitRise server. A
// gate code is a key to the property, so:
//
//   - Code shows only a code's last two digits (the dashboard's Gate codes
//     view; enough to tell two codes apart, not enough to open the gate);
//   - DigitRuns hides every run of three or more digits in free text (the
//     consume output), which covers any code length the gate software uses.
package mask

import "strings"

const hidden = "•"

// Code returns the code with all but its last two characters hidden. A code
// of two characters or fewer is hidden completely.
func Code(code string) string {
	r := []rune(code)
	if len(r) == 0 {
		return ""
	}
	if len(r) <= 2 {
		return strings.Repeat(hidden, len(r))
	}
	return strings.Repeat(hidden, len(r)-2) + string(r[len(r)-2:])
}

// DigitRuns replaces every run of three or more ASCII digits with the same
// number of asterisks ("0:258" reads "0:***", "482913" reads "******").
func DigitRuns(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); {
		if b[i] < '0' || b[i] > '9' {
			i++
			continue
		}
		j := i
		for j < len(b) && b[j] >= '0' && b[j] <= '9' {
			j++
		}
		if j-i >= 3 {
			for k := i; k < j; k++ {
				b[k] = '*'
			}
		}
		i = j
	}
	return string(b)
}
