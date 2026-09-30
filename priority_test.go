package namedotcom

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/libdns/libdns"
)

// A zero priority is valid (and common for SRV), so it must be sent as an
// explicit "priority":0 rather than dropped by omitempty.
func TestFromLibDNSRecordSendsZeroPriority(t *testing.T) {
	for _, rr := range []libdns.RR{
		{Name: "_autodiscover._tcp", Type: "SRV", Data: "0 0 443 mail.example.com."},
		{Name: "@", Type: "MX", Data: "0 mail.example.com."},
	} {
		var rec nameDotComRecord
		rec.fromLibDNSRecord(0, rr, "example.com")
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if !strings.Contains(string(body), `"priority":0`) {
			t.Errorf("%s: request body missing \"priority\":0: %s", rr.Type, body)
		}
	}
}

// Record types without a priority must not gain one.
func TestFromLibDNSRecordOmitsPriorityForOtherTypes(t *testing.T) {
	var rec nameDotComRecord
	rec.fromLibDNSRecord(0, libdns.RR{Name: "www", Type: "A", Data: "192.0.2.1"}, "example.com")
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(body), `"priority"`) {
		t.Errorf("request body has a spurious priority field: %s", body)
	}
}
