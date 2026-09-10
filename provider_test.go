package namedotcom

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

const testZone = "example.com"

// newTestProvider returns a Provider wired to an in-process name.com CORE API
// mock (see mock_test.go) seeded with the sanitized fixture records.
func newTestProvider(t *testing.T) *Provider {
	t.Helper()

	mock := newMockNameCom(t)
	srv := httptest.NewServer(mock.handler())
	t.Cleanup(srv.Close)

	p := &Provider{
		Token: mockToken,
		User:  mockUser,
	}
	p.client = &nameDotCom{
		Server: srv.URL,
		User:   mockUser,
		Token:  mockToken,
		client: &http.Client{Timeout: 10 * time.Second},
	}
	return p
}

func TestProvider_ListZones(t *testing.T) {
	p := newTestProvider(t)

	zones, err := p.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones() error = %v", err)
	}

	want := map[string]bool{"example.com": false, "example.net": false}
	for _, zone := range zones {
		if _, ok := want[zone.Name]; !ok {
			t.Fatalf("ListZones() returned unexpected zone %q", zone.Name)
		}
		want[zone.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("ListZones() missing zone %q (got %v)", name, zones)
		}
	}
}

func TestProvider_GetRecords(t *testing.T) {
	p := newTestProvider(t)

	got, err := p.GetRecords(context.Background(), testZone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}

	if len(got) != 13 {
		t.Fatalf("GetRecords() returned %d records, want 13", len(got))
	}

	find := func(name, typ string) libdns.Record {
		for _, rec := range got {
			rr := rec.RR()
			if strings.EqualFold(rr.Name, name) && rr.Type == typ {
				return rec
			}
		}
		t.Fatalf("GetRecords() missing record %s %s", typ, name)
		return nil
	}

	// Apex A record: relative name is "@" and the type is Address.
	apexA, ok := find("@", "A").(libdns.Address)
	if !ok {
		t.Fatalf("apex A record is not a libdns.Address: %T", find("@", "A"))
	}
	if apexA.IP != netip.MustParseAddr("192.0.2.3") || apexA.TTL != 300*time.Second {
		t.Errorf("apex A record = %+v, want IP 192.0.2.3 TTL 300s", apexA)
	}

	// CNAME records map to libdns.CNAME with relative names.
	www, ok := find("www", "CNAME").(libdns.CNAME)
	if !ok {
		t.Fatalf("www record is not a libdns.CNAME: %T", find("www", "CNAME"))
	}
	if www.Target != "example.com" {
		t.Errorf("www CNAME target = %q, want %q", www.Target, "example.com")
	}

	// TXT records map to libdns.TXT.
	apexTxt, ok := find("@", "TXT").(libdns.TXT)
	if !ok {
		t.Fatalf("apex TXT record is not a libdns.TXT: %T", find("@", "TXT"))
	}
	if apexTxt.Text != "v=spf1 -all" {
		t.Errorf("apex TXT = %q, want %q", apexTxt.Text, "v=spf1 -all")
	}

	// Wildcard A records keep their wildcard label.
	wild, ok := find("*.vms", "A").(libdns.Address)
	if !ok {
		t.Fatalf("wildcard record is not a libdns.Address: %T", find("*.vms", "A"))
	}
	if wild.IP != netip.MustParseAddr("192.0.2.8") {
		t.Errorf("wildcard A IP = %v, want 192.0.2.8", wild.IP)
	}

	// ANAME is not modeled by libdns; it must come back as an opaque RR,
	// not an error.
	aname, ok := find("share", "ANAME").(libdns.RR)
	if !ok {
		t.Fatalf("ANAME record is not an opaque libdns.RR: %T", find("share", "ANAME"))
	}
	if aname.Data != "m.example.net" {
		t.Errorf("ANAME data = %q, want %q", aname.Data, "m.example.net")
	}
}

func TestProvider_AppendRecords(t *testing.T) {
	p := newTestProvider(t)

	newRecords := []libdns.Record{
		libdns.Address{
			Name: "test-append-a",
			TTL:  300 * time.Second,
			IP:   netip.MustParseAddr("192.0.2.40"),
		},
		libdns.TXT{
			Name: "test-append-txt",
			TTL:  300 * time.Second,
			Text: "appended by test",
		},
	}

	got, err := p.AppendRecords(context.Background(), testZone, newRecords)
	if err != nil {
		t.Fatalf("AppendRecords() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("AppendRecords() returned %d records, want 2", len(got))
	}

	addr, ok := got[0].(libdns.Address)
	if !ok || addr.Name != "test-append-a" || addr.IP != netip.MustParseAddr("192.0.2.40") {
		t.Errorf("AppendRecords()[0] = %+v (%T), want Address test-append-a 192.0.2.40", got[0], got[0])
	}
	txt, ok := got[1].(libdns.TXT)
	if !ok || txt.Name != "test-append-txt" || txt.Text != "appended by test" {
		t.Errorf("AppendRecords()[1] = %+v (%T), want TXT test-append-txt", got[1], got[1])
	}

	// The records must now be visible in the zone.
	all, err := p.GetRecords(context.Background(), testZone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	if len(all) != 15 {
		t.Errorf("zone has %d records after append, want 15", len(all))
	}

	// Appending a record that already exists must fail without changing it.
	_, err = p.AppendRecords(context.Background(), testZone, newRecords[:1])
	if err == nil {
		t.Fatal("AppendRecords() duplicate succeeded, want error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("AppendRecords() duplicate error = %q, want mention of existing record", err)
	}
}

func TestProvider_SetRecords(t *testing.T) {
	p := newTestProvider(t)

	tests := []struct {
		name    string
		records []libdns.Record
	}{
		{
			// Updates the existing www CNAME in place (PUT path).
			name: "update existing cname",
			records: []libdns.Record{
				libdns.CNAME{
					Name:   "www",
					TTL:    300 * time.Second,
					Target: "new-target.example.net",
				},
			},
		},
		{
			// Creates a new SRV record (POST path) and verifies the round trip.
			name: "create srv",
			records: []libdns.Record{
				libdns.SRV{
					Service:   "sip",
					Transport: "tcp",
					Name:      "test-srv",
					TTL:       300 * time.Second,
					Priority:  10,
					Weight:    5,
					Port:      443,
					Target:    "sip.example.net.",
				},
			},
		},
		{
			// Creates a new MX record (POST path) and verifies the round trip.
			name: "create mx",
			records: []libdns.Record{
				libdns.MX{
					Name:       "test-mx",
					TTL:        300 * time.Second,
					Preference: 20,
					Target:     "mail.example.net.",
				},
			},
		},
		{
			// Replaces the apex A RRset: one existing record (192.0.2.3) is
			// updated and a second record is created; no other A records may
			// remain at the apex.
			name: "replace rrset",
			records: []libdns.Record{
				libdns.Address{
					Name: "@",
					TTL:  300 * time.Second,
					IP:   netip.MustParseAddr("192.0.2.50"),
				},
				libdns.Address{
					Name: "@",
					TTL:  300 * time.Second,
					IP:   netip.MustParseAddr("192.0.2.51"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.SetRecords(context.Background(), testZone, tt.records)
			if err != nil {
				t.Fatalf("SetRecords() error = %v", err)
			}
			if len(got) != len(tt.records) {
				t.Fatalf("SetRecords() returned %d records, want %d", len(got), len(tt.records))
			}

			all, err := p.GetRecords(context.Background(), testZone)
			if err != nil {
				t.Fatalf("GetRecords() error = %v", err)
			}

			switch tt.name {
			case "update existing cname":
				cname, ok := findRecord(t, all, "www", "CNAME").(libdns.CNAME)
				if !ok || cname.Target != "new-target.example.net" {
					t.Errorf("www CNAME after set = %+v (%T), want target new-target.example.net", all, cname)
				}
			case "create srv":
				srv, ok := findRecord(t, all, "_sip._tcp.test-srv", "SRV").(libdns.SRV)
				if !ok {
					t.Fatalf("SRV record after set is %T, want libdns.SRV", findRecord(t, all, "_sip._tcp.test-srv", "SRV"))
				}
				want := libdns.SRV{
					Service:   "sip",
					Transport: "tcp",
					Name:      "test-srv",
					TTL:       300 * time.Second,
					Priority:  10,
					Weight:    5,
					Port:      443,
					Target:    "sip.example.net",
				}
				if srv != want {
					t.Errorf("SRV round trip = %+v, want %+v", srv, want)
				}
			case "create mx":
				mx, ok := findRecord(t, all, "test-mx", "MX").(libdns.MX)
				if !ok {
					t.Fatalf("MX record after set is %T, want libdns.MX", findRecord(t, all, "test-mx", "MX"))
				}
				want := libdns.MX{
					Name:       "test-mx",
					TTL:        300 * time.Second,
					Preference: 20,
					Target:     "mail.example.net",
				}
				if mx != want {
					t.Errorf("MX round trip = %+v, want %+v", mx, want)
				}
			case "replace rrset":
				var apexA []libdns.Address
				for _, rec := range all {
					addr, ok := rec.(libdns.Address)
					if ok && addr.Name == "@" {
						apexA = append(apexA, addr)
					}
				}
				if len(apexA) != 2 {
					t.Fatalf("apex A records after set = %v, want exactly 2", apexA)
				}
				ips := map[string]bool{apexA[0].IP.String(): true, apexA[1].IP.String(): true}
				if !ips["192.0.2.50"] || !ips["192.0.2.51"] {
					t.Errorf("apex A records after set = %v, want 192.0.2.50 and 192.0.2.51", apexA)
				}
			}
		})
	}
}

func TestProvider_DeleteRecords(t *testing.T) {
	p := newTestProvider(t)

	existing := libdns.CNAME{
		Name:   "git",
		TTL:    300 * time.Second,
		Target: "tls.example.net",
	}
	missing := libdns.TXT{
		Name: "does-not-exist",
		TTL:  300 * time.Second,
		Text: "nope",
	}

	got, err := p.DeleteRecords(context.Background(), testZone, []libdns.Record{existing, missing})
	if err != nil {
		t.Fatalf("DeleteRecords() error = %v", err)
	}

	// Only the record that existed in the zone is returned; the missing one
	// is silently ignored.
	if len(got) != 1 {
		t.Fatalf("DeleteRecords() returned %d records, want 1 (got %+v)", len(got), got)
	}
	deleted, ok := got[0].(libdns.CNAME)
	if !ok || deleted.Name != "git" || deleted.Target != "tls.example.net" {
		t.Errorf("DeleteRecords()[0] = %+v (%T), want CNAME git tls.example.net", got[0], got[0])
	}

	all, err := p.GetRecords(context.Background(), testZone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	for _, rec := range all {
		if rec.RR().Name == "git" && rec.RR().Type == "CNAME" {
			t.Error("git CNAME still present after DeleteRecords")
		}
	}
}

func TestProvider_AuthFailure(t *testing.T) {
	p := newTestProvider(t)
	p.client.Token = "wrong-token"

	if _, err := p.GetRecords(context.Background(), testZone); err == nil {
		t.Fatal("GetRecords() with bad token succeeded, want error")
	}
}

func TestProvider_ConcurrentUse(t *testing.T) {
	p := newTestProvider(t)

	const workers = 10
	var wg sync.WaitGroup
	for i := range workers {
		n := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := p.GetRecords(context.Background(), testZone); err != nil {
				t.Errorf("concurrent GetRecords() error = %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			rec := libdns.TXT{
				Name: fmt.Sprintf("test-conc-%d", n),
				TTL:  300 * time.Second,
				Text: "concurrent",
			}
			if _, err := p.AppendRecords(context.Background(), testZone, []libdns.Record{rec}); err != nil {
				t.Errorf("concurrent AppendRecords() error = %v", err)
			}
		}()
	}
	wg.Wait()

	all, err := p.GetRecords(context.Background(), testZone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	if len(all) != 13+workers {
		t.Errorf("zone has %d records after concurrent appends, want %d", len(all), 13+workers)
	}
}

// findRecord returns the first record in recs matching name and type.
func findRecord(t *testing.T, recs []libdns.Record, name, typ string) libdns.Record {
	t.Helper()
	for _, rec := range recs {
		rr := rec.RR()
		if strings.EqualFold(rr.Name, name) && rr.Type == typ {
			return rec
		}
	}
	t.Fatalf("record %s %s not found in %v", typ, name, recs)
	return nil
}
