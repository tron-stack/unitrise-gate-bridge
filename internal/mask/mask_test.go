package mask

import "testing"

func TestCodeShowsOnlyTheLastTwo(t *testing.T) {
	cases := map[string]string{
		"482913":     "••••13",
		"9876543210": "••••••••10",
		"1234":       "••34",
		"123":        "•23",
		"12":         "••",
		"7":          "•",
		"":           "",
	}
	for in, want := range cases {
		if got := Code(in); got != want {
			t.Errorf("Code(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDigitRunsHidesThreeOrMore(t *testing.T) {
	cases := map[string]string{
		"A,482913,A14,011A":            "A,******,A14,***A",
		"result 0:258":                 "result 0:***",
		"exit 1, 12 lines":             "exit 1, 12 lines",
		"code 9876543210 sent":         "code ********** sent",
		"":                             "",
		"no digits here":               "no digits here",
		"12-34-567":                    "12-34-***",
		"CONSUMED-ARGS-0":              "CONSUMED-ARGS-0",
		"Kein Zugriff: Tür 4821 offen": "Kein Zugriff: Tür **** offen",
	}
	for in, want := range cases {
		if got := DigitRuns(in); got != want {
			t.Errorf("DigitRuns(%q) = %q, want %q", in, got, want)
		}
	}
}
