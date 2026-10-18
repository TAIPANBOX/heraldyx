package config

import "testing"

// The operator's HERALDYX_SMTP_TLS reaches the transport as written. What it
// means, and whether it is refused, is decided where the transport is built,
// so one place holds the rule and this test only proves the wire.
func TestTheSMTPTLSSettingIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("HERALDYX_SMTP_TLS", "implicit")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.SMTPTLS != "implicit" {
		t.Fatalf("SMTPTLS = %q, want %q", c.SMTPTLS, "implicit")
	}
}
