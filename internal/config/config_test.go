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
