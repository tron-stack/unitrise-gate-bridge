package config

import (
	"strings"
	"testing"
)

func base() *Config {
	return &Config{AccessKey: "k", AccessSecret: "s", FacilityID: "f", SavePath: "/tmp/x", APIEndpoint: "https://app.mytruckyards.com"}
}

func TestValidateComplete(t *testing.T) {
	if err := base().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNamesEveryMissingField(t *testing.T) {
	err := (&Config{}).Validate()
	if err == nil {
		t.Fatal("empty config must not validate")
	}
	for _, f := range []string{"accessKey", "accessSecret", "facilityId", "savePath"} {
		if !strings.Contains(err.Error(), f) {
			t.Fatalf("error should name %s: %v", f, err)
		}
	}
}

func TestValidateRefusesPlainHTTP(t *testing.T) {
	c := base()
	c.APIEndpoint = "http://app.mytruckyards.com"
	if err := c.Validate(); err == nil {
		t.Fatal("plain http to a non-loopback host must be refused - the HMAC headers would cross the wire in the clear")
	}
}

func TestValidateAllowsLoopbackHTTP(t *testing.T) {
	for _, ep := range []string{"http://127.0.0.1:4000", "http://localhost:4000", "http://[::1]:4000"} {
		c := base()
		c.APIEndpoint = ep
		if err := c.Validate(); err != nil {
			t.Fatalf("loopback http must stay allowed for local testing (%s): %v", ep, err)
		}
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	cases := map[string]string{
		// Household names of EITHER brand mean "the UnitRise API" - handing
		// the SPA host to the agent fails with a confusing 404 otherwise.
		"unitrise.com":              DefaultAPIEndpoint,
		"https://www.unitrise.com/": DefaultAPIEndpoint,
		"api.unitrise.com":          DefaultAPIEndpoint,
		"app.mytruckyards.com":      DefaultAPIEndpoint, // the old broken default
		"MYTRUCKYARDS.COM":          DefaultAPIEndpoint,
		// Real hosts pass through (scheme added when missing).
		"truckpark-backend.onrender.com": "https://truckpark-backend.onrender.com",
		"https://example.com":            "https://example.com",
		"http://127.0.0.1:4000":          "http://127.0.0.1:4000",
		"":                               "",
		"  ":                             "",
	}
	for in, want := range cases {
		if got := NormalizeEndpoint(in); got != want {
			t.Fatalf("NormalizeEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
