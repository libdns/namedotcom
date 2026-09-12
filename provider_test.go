package namedotcom

// provider_test.go — unified test suite that runs against either the
// in-process mock (default) or a real/external name.com API
// (NAMEDOTCOM_LIVE=true). See live_test.go for the full description.
//
// All write tests use names prefixed with testPrefix ("_libdnstest-") so they
// cannot collide with real records and are cleaned up via t.Cleanup.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// ---------------------------------------------------------------------------
// Provider factory — mock or live
// ---------------------------------------------------------------------------

// newTestProvider returns a Provider wired to either the in-process mock
// (default) or a live/external API (when NAMEDOTCOM_LIVE=true).
func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	if isLiveMode() {
		return newLiveProvider(t)
	}
	return newMockProvider(t)
}

// newMockProvider starts an in-process mock name.com API seeded with fixture
// data from testdata/.
func newMockProvider(t *testing.T) *Provider {
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

// newLiveProvider creates a Provider for the real (or external) API using
// credentials from the environment.
func newLiveProvider(t *testing.T) *Provider {
	t.Helper()
	user := os.Getenv("NAMEDOTCOM_USERNAME")
	token := os.Getenv("NAMEDOTCOM_TOKEN")
	if user == "" || token == "" {
		t.Skip("NAMEDOTCOM_LIVE=true but NAMEDOTCOM_USERNAME/NAMEDOTCOM_TOKEN not set")
	}
	return &Provider{
		Token:  token,
		User:   user,
		Server: liveServer(),
	}
}

// testZoneName returns the zone name to use for testing. In mock mode this is
// always mockZoneName ("example.com"). In live mode it uses the zone selected
// by TestMain's backup (or NAMEDOTCOM_TEST_ZONE, or the first ListZones result).
func testZoneName(t *testing.T, p *Provider) string {
	t.Helper()
	if isLiveMode() {
		return liveTestZone(t, p)
	}
	return mockZoneName
}

// ---------------------------------------------------------------------------
// Read-only tests
// ---------------------------------------------------------------------------

func TestListZones(t *testing.T) {
	p := newTestProvider(t)

	zones, err := p.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones() error = %v", err)
	}
	if len(zones) == 0 {
		t.Fatal("ListZones() returned 0 zones")
	}

	t.Logf("ListZones() returned %d zone(s):", len(zones))
	for _, z := range zones {
		t.Logf("  - %s", z.Name)
	}

	// Mock-specific: verify the fixture domains are present.
	if !isLiveMode() {
		want := map[string]bool{"example.com": false, "example.net": false}
		for _, z := range zones {
			if _, ok := want[z.Name]; ok {
				want[z.Name] = true
			}
		}
		for name, seen := range want {
			if !seen {
				t.Errorf("ListZones() missing zone %q", name)
			}
		}
	}
}

func TestGetRecords(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)

	got, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	if len(got) == 0 {
		t.Fatal("GetRecords() returned 0 records")
	}

	t.Logf("GetRecords() returned %d record(s)", len(got))

	// Verify every record has a type and non-negative TTL.
	for _, rec := range got {
		rr := rec.RR()
		if rr.Type == "" {
			t.Errorf("record has empty type: %+v", rec)
		}
		if rr.TTL < 0 {
			t.Errorf("record %s %s has negative TTL: %v", rr.Type, rr.Name, rr.TTL)
		}
	}

	// Mock-specific: verify the fixture records and record-type mapping.
	if !isLiveMode() {
		if len(got) != 13 {
			t.Fatalf("GetRecords() returned %d records, want 13", len(got))
		}

		// Apex A record.
		apexA, ok := findRecordOpt(got, "@", "A").(libdns.Address)
		if !ok {
			t.Fatalf("apex A record not found or wrong type")
		}
		if apexA.IP != netip.MustParseAddr("192.0.2.3") || apexA.TTL != 300*time.Second {
			t.Errorf("apex A = %+v, want IP 192.0.2.3 TTL 300s", apexA)
		}

		// CNAME with relative name.
		www, ok := findRecordOpt(got, "www", "CNAME").(libdns.CNAME)
		if !ok {
			t.Fatalf("www CNAME not found or wrong type")
		}
		if www.Target != "example.com" {
			t.Errorf("www CNAME target = %q, want %q", www.Target, "example.com")
		}

		// TXT record.
		apexTxt, ok := findRecordOpt(got, "@", "TXT").(libdns.TXT)
		if !ok {
			t.Fatalf("apex TXT not found or wrong type")
		}
		if apexTxt.Text != "v=spf1 -all" {
			t.Errorf("apex TXT = %q, want %q", apexTxt.Text, "v=spf1 -all")
		}

		// Wildcard A record keeps its wildcard label.
		wild, ok := findRecordOpt(got, "*.vms", "A").(libdns.Address)
		if !ok {
			t.Fatalf("wildcard *.vms A not found or wrong type")
		}
		if wild.IP != netip.MustParseAddr("192.0.2.8") {
			t.Errorf("wildcard A IP = %v, want 192.0.2.8", wild.IP)
		}

		// ANAME comes back as opaque RR (not a modelled libdns type).
		aname, ok := findRecordOpt(got, "share", "ANAME").(libdns.RR)
		if !ok {
			t.Fatalf("ANAME record is not an opaque libdns.RR: %T", findRecordOpt(got, "share", "ANAME"))
		}
		if aname.Data != "m.example.net" {
			t.Errorf("ANAME data = %q, want %q", aname.Data, "m.example.net")
		}
	}
}

func TestAuthFailure(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)

	// Set a bad token on both the Provider field and any pre-built client.
	p.Token = "wrong-token"
	if p.client != nil {
		p.client.Token = "wrong-token"
	}

	if _, err := p.GetRecords(context.Background(), zone); err == nil {
		t.Fatal("GetRecords() with bad token succeeded, want error")
	}
}

// ---------------------------------------------------------------------------
// AppendRecords (subdomain)
// ---------------------------------------------------------------------------

func TestAppendRecords(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)
	t.Cleanup(func() { cleanupTestRecords(t, p, zone) })

	// Capture the record count before so we can verify a relative increase.
	before, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() before: %v", err)
	}

	newRecords := []libdns.Record{
		libdns.Address{
			Name: testPrefix + "-append-a",
			TTL:  300 * time.Second,
			IP:   netip.MustParseAddr("192.0.2.40"),
		},
		libdns.TXT{
			Name: testPrefix + "-append-txt",
			TTL:  300 * time.Second,
			Text: "appended by test",
		},
	}

	got, err := p.AppendRecords(context.Background(), zone, newRecords)
	if err != nil {
		t.Fatalf("AppendRecords() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("AppendRecords() returned %d, want 2", len(got))
	}

	// Verify the records are visible via GetRecords.
	all, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() after: %v", err)
	}
	if len(all) != len(before)+2 {
		t.Errorf("record count = %d, want %d (before %d + 2)", len(all), len(before)+2, len(before))
	}

	addr, ok := findRecordOpt(all, testPrefix+"-append-a", "A").(libdns.Address)
	if !ok {
		t.Fatalf("appended A record not found or wrong type")
	}
	if addr.IP != netip.MustParseAddr("192.0.2.40") {
		t.Errorf("appended A IP = %v, want 192.0.2.40", addr.IP)
	}

	txt, ok := findRecordOpt(all, testPrefix+"-append-txt", "TXT").(libdns.TXT)
	if !ok {
		t.Fatalf("appended TXT record not found or wrong type")
	}
	if txt.Text != "appended by test" {
		t.Errorf("appended TXT = %q, want %q", txt.Text, "appended by test")
	}
}

func TestAppendRecords_Duplicate(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)
	t.Cleanup(func() { cleanupTestRecords(t, p, zone) })

	rec := libdns.Address{
		Name: testPrefix + "-dup",
		TTL:  300 * time.Second,
		IP:   netip.MustParseAddr("192.0.2.41"),
	}

	if _, err := p.AppendRecords(context.Background(), zone, []libdns.Record{rec}); err != nil {
		t.Fatalf("first AppendRecords() error = %v", err)
	}

	_, err := p.AppendRecords(context.Background(), zone, []libdns.Record{rec})
	if err == nil {
		t.Fatal("duplicate AppendRecords() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate error = %q, want mention of 'already exists'", err)
	}
}

// ---------------------------------------------------------------------------
// SetRecords (subdomain)
// ---------------------------------------------------------------------------

func TestSetRecords(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)
	t.Cleanup(func() { cleanupTestRecords(t, p, zone) })

	t.Run("CreateSRV", func(t *testing.T) {
		rec := libdns.SRV{
			Service:   "sip",
			Transport: "tcp",
			Name:      testPrefix + "-srv",
			TTL:       300 * time.Second,
			Priority:  10,
			Weight:    5,
			Port:      443,
			Target:    "sip.example.net.",
		}

		got, err := p.SetRecords(context.Background(), zone, []libdns.Record{rec})
		if err != nil {
			t.Fatalf("SetRecords() error = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("SetRecords() returned %d, want 1", len(got))
		}

		all, err := p.GetRecords(context.Background(), zone)
		if err != nil {
			t.Fatalf("GetRecords() error = %v", err)
		}
		srv, ok := findRecordOpt(all, "_sip._tcp."+testPrefix+"-srv", "SRV").(libdns.SRV)
		if !ok {
			t.Fatalf("SRV record not found or wrong type")
		}
		want := libdns.SRV{
			Service:   "sip",
			Transport: "tcp",
			Name:      testPrefix + "-srv",
			TTL:       300 * time.Second,
			Priority:  10,
			Weight:    5,
			Port:      443,
			Target:    "sip.example.net",
		}
		if srv != want {
			t.Errorf("SRV round trip = %+v, want %+v", srv, want)
		}
	})

	t.Run("CreateMX", func(t *testing.T) {
		rec := libdns.MX{
			Name:       testPrefix + "-mx",
			TTL:        300 * time.Second,
			Preference: 20,
			Target:     "mail.example.net.",
		}

		got, err := p.SetRecords(context.Background(), zone, []libdns.Record{rec})
		if err != nil {
			t.Fatalf("SetRecords() error = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("SetRecords() returned %d, want 1", len(got))
		}

		all, err := p.GetRecords(context.Background(), zone)
		if err != nil {
			t.Fatalf("GetRecords() error = %v", err)
		}
		mx, ok := findRecordOpt(all, testPrefix+"-mx", "MX").(libdns.MX)
		if !ok {
			t.Fatalf("MX record not found or wrong type")
		}
		want := libdns.MX{
			Name:       testPrefix + "-mx",
			TTL:        300 * time.Second,
			Preference: 20,
			Target:     "mail.example.net",
		}
		if mx != want {
			t.Errorf("MX round trip = %+v, want %+v", mx, want)
		}
	})

	t.Run("UpdateCNAME", func(t *testing.T) {
		name := testPrefix + "-cname"

		// First create a CNAME (POST path).
		original := libdns.CNAME{
			Name:   name,
			TTL:    300 * time.Second,
			Target: "old-target.example.net",
		}
		if _, err := p.AppendRecords(context.Background(), zone, []libdns.Record{original}); err != nil {
			t.Fatalf("AppendRecords() error = %v", err)
		}

		// Now update it via SetRecords (PUT path).
		updated := libdns.CNAME{
			Name:   name,
			TTL:    300 * time.Second,
			Target: "new-target.example.net",
		}
		got, err := p.SetRecords(context.Background(), zone, []libdns.Record{updated})
		if err != nil {
			t.Fatalf("SetRecords() error = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("SetRecords() returned %d, want 1", len(got))
		}

		all, err := p.GetRecords(context.Background(), zone)
		if err != nil {
			t.Fatalf("GetRecords() error = %v", err)
		}
		cname, ok := findRecordOpt(all, name, "CNAME").(libdns.CNAME)
		if !ok {
			t.Fatalf("CNAME record not found or wrong type")
		}
		if cname.Target != "new-target.example.net" {
			t.Errorf("CNAME target = %q, want %q", cname.Target, "new-target.example.net")
		}
	})

	t.Run("ReplaceRRset", func(t *testing.T) {
		name := testPrefix + "-rrset"

		// Start with 3 A records at the same subdomain name.
		initial := []libdns.Record{
			libdns.Address{Name: name, TTL: 300 * time.Second, IP: netip.MustParseAddr("192.0.2.60")},
			libdns.Address{Name: name, TTL: 300 * time.Second, IP: netip.MustParseAddr("192.0.2.61")},
			libdns.Address{Name: name, TTL: 300 * time.Second, IP: netip.MustParseAddr("192.0.2.62")},
		}
		if _, err := p.AppendRecords(context.Background(), zone, initial); err != nil {
			t.Fatalf("AppendRecords() error = %v", err)
		}

		// Replace with only 2 A records (different IPs). SetRecords must
		// update 2 of the 3 and delete the surplus.
		replacement := []libdns.Record{
			libdns.Address{Name: name, TTL: 300 * time.Second, IP: netip.MustParseAddr("192.0.2.70")},
			libdns.Address{Name: name, TTL: 300 * time.Second, IP: netip.MustParseAddr("192.0.2.71")},
		}
		got, err := p.SetRecords(context.Background(), zone, replacement)
		if err != nil {
			t.Fatalf("SetRecords() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("SetRecords() returned %d, want 2", len(got))
		}

		all, err := p.GetRecords(context.Background(), zone)
		if err != nil {
			t.Fatalf("GetRecords() error = %v", err)
		}
		var aRecs []libdns.Address
		for _, rec := range all {
			if addr, ok := rec.(libdns.Address); ok && addr.Name == name {
				aRecs = append(aRecs, addr)
			}
		}
		if len(aRecs) != 2 {
			t.Fatalf("found %d A records at %s, want 2", len(aRecs), name)
		}
		ips := map[string]bool{aRecs[0].IP.String(): true, aRecs[1].IP.String(): true}
		if !ips["192.0.2.70"] || !ips["192.0.2.71"] {
			t.Errorf("A records = %v, want 192.0.2.70 and 192.0.2.71", aRecs)
		}
		for _, a := range aRecs {
			switch a.IP.String() {
			case "192.0.2.60", "192.0.2.61", "192.0.2.62":
				t.Errorf("old IP %s still present after replace", a.IP)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// DeleteRecords (subdomain)
// ---------------------------------------------------------------------------

func TestDeleteRecords(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)
	t.Cleanup(func() { cleanupTestRecords(t, p, zone) })

	// Create a record to delete.
	rec := libdns.TXT{
		Name: testPrefix + "-delete",
		TTL:  300 * time.Second,
		Text: "to be deleted",
	}
	if _, err := p.AppendRecords(context.Background(), zone, []libdns.Record{rec}); err != nil {
		t.Fatalf("AppendRecords() error = %v", err)
	}

	got, err := p.DeleteRecords(context.Background(), zone, []libdns.Record{rec})
	if err != nil {
		t.Fatalf("DeleteRecords() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("DeleteRecords() returned %d, want 1", len(got))
	}

	all, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	if findRecordOpt(all, testPrefix+"-delete", "TXT") != nil {
		t.Error("record still present after DeleteRecords")
	}
}

func TestDeleteRecords_Missing(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)

	missing := libdns.TXT{
		Name: testPrefix + "-nonexistent",
		TTL:  300 * time.Second,
		Text: "never existed",
	}

	got, err := p.DeleteRecords(context.Background(), zone, []libdns.Record{missing})
	if err != nil {
		t.Fatalf("DeleteRecords() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("DeleteRecords() returned %d for missing record, want 0", len(got))
	}
}

// ---------------------------------------------------------------------------
// Concurrent use (subdomain)
// ---------------------------------------------------------------------------

func TestConcurrentAppend(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)
	t.Cleanup(func() { cleanupTestRecords(t, p, zone) })

	const workers = 10
	var wg sync.WaitGroup
	for i := range workers {
		n := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := p.GetRecords(context.Background(), zone); err != nil {
				t.Errorf("concurrent GetRecords() error = %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			rec := libdns.TXT{
				Name: fmt.Sprintf("%s-conc-%d", testPrefix, n),
				TTL:  300 * time.Second,
				Text: "concurrent",
			}
			if _, err := p.AppendRecords(context.Background(), zone, []libdns.Record{rec}); err != nil {
				t.Errorf("concurrent AppendRecords() error = %v", err)
			}
		}()
	}
	wg.Wait()

	all, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}

	count := 0
	for _, rec := range all {
		if strings.HasPrefix(rec.RR().Name, testPrefix+"-conc-") {
			count++
		}
	}
	if count != workers {
		t.Errorf("found %d concurrent test records, want %d", count, workers)
	}
}

// ---------------------------------------------------------------------------
// Full round-trip: set -> read -> delete -> set back (as read) -> read -> verify
// ---------------------------------------------------------------------------

// TestRoundTrip_AllTypes creates one record of every supported type (A, AAAA,
// CNAME, MX, NS, SRV, TXT, ANAME), reads them back as the API presents them,
// deletes them, then sets them back using the exact records that were read.
// This catches edge cases where the read-back representation (e.g. ANAME as
// opaque libdns.RR, SRV with decomposed fields, MX priority in a separate
// field) might not survive being passed back through fromLibDNSRecord.
//
// Flow:
//  1. SetRecords    - create one of every type using typed libdns structs.
//  2. GetRecords    - read them back as the API presents them.
//  3. DeleteRecords - delete using the read-back records.
//  4. SetRecords    - set them back using the read-back records ("as presented").
//  5. GetRecords    - read again and verify the records match the first read.
//  6. DeleteRecords - delete all test records (restore original state).
func TestRoundTrip_AllTypes(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)
	ctx := context.Background()
	t.Cleanup(func() { cleanupTestRecords(t, p, zone) })

	// One record of every supported type, using subdomain names with the
	// test prefix so they cannot collide with real records.
	testRecords := []libdns.Record{
		libdns.Address{
			Name: testPrefix + "-rt-a",
			TTL:  300 * time.Second,
			IP:   netip.MustParseAddr("192.0.2.10"),
		},
		libdns.Address{
			Name: testPrefix + "-rt-aaaa",
			TTL:  300 * time.Second,
			IP:   netip.MustParseAddr("2001:db8::1"),
		},
		libdns.CNAME{
			Name:   testPrefix + "-rt-cname",
			TTL:    300 * time.Second,
			Target: "target.example.net.",
		},
		libdns.MX{
			Name:       testPrefix + "-rt-mx",
			TTL:        300 * time.Second,
			Preference: 10,
			Target:     "mail.example.net.",
		},
		libdns.NS{
			Name:   testPrefix + "-rt-ns",
			TTL:    300 * time.Second,
			Target: "ns1.example.net.",
		},
		libdns.SRV{
			Service:   "sip",
			Transport: "tcp",
			Name:      testPrefix + "-rt-srv",
			TTL:       300 * time.Second,
			Priority:  10,
			Weight:    5,
			Port:      443,
			Target:    "sip.example.net.",
		},
		libdns.TXT{
			Name: testPrefix + "-rt-txt",
			TTL:  300 * time.Second,
			Text: "round-trip test",
		},
		libdns.RR{
			Name: testPrefix + "-rt-aname",
			TTL:  300 * time.Second,
			Type: "ANAME",
			Data: "aname.example.net.",
		},
	}

	// ---- Phase 1: SetRecords - create one of every type ----
	t.Log("Phase 1: SetRecords - create one of every type")
	created, err := p.SetRecords(ctx, zone, testRecords)
	if err != nil {
		t.Fatalf("SetRecords() error = %v", err)
	}
	if len(created) != len(testRecords) {
		t.Fatalf("SetRecords() returned %d records, want %d", len(created), len(testRecords))
	}

	// ---- Phase 2: GetRecords - read them back as the API presents them ----
	t.Log("Phase 2: GetRecords - read back as presented")
	afterCreate, err := p.GetRecords(ctx, zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	readBack := collectRoundTripRecords(afterCreate)
	if len(readBack) != len(testRecords) {
		t.Fatalf("found %d round-trip test records after create, want %d", len(readBack), len(testRecords))
	}
	t.Logf("read back %d records:", len(readBack))
	for _, rec := range readBack {
		rr := rec.RR()
		t.Logf("  %s  %-6s  %s", rr.Name, rr.Type, rr.Data)
	}

	// ---- Phase 3: DeleteRecords - delete using the read-back records ----
	t.Log("Phase 3: DeleteRecords - delete as presented")
	deleted, err := p.DeleteRecords(ctx, zone, readBack)
	if err != nil {
		t.Fatalf("DeleteRecords() error = %v", err)
	}
	if len(deleted) != len(readBack) {
		t.Fatalf("DeleteRecords() returned %d, want %d", len(deleted), len(readBack))
	}

	// Verify they're gone.
	afterDelete, err := p.GetRecords(ctx, zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	if leftover := collectRoundTripRecords(afterDelete); len(leftover) > 0 {
		t.Fatalf("%d round-trip records still present after delete", len(leftover))
	}

	// ---- Phase 4: SetRecords - set them back using the read-back records ----
	// This is the critical step: we pass the records exactly as they were
	// read back from the API, not the original typed structs. ANAME comes
	// back as opaque libdns.RR; SRV has decomposed Service/Transport/Name;
	// MX priority is in Preference. All must survive fromLibDNSRecord.
	t.Log("Phase 4: SetRecords - set back as presented")
	reSet, err := p.SetRecords(ctx, zone, readBack)
	if err != nil {
		t.Fatalf("SetRecords() error = %v", err)
	}
	if len(reSet) != len(readBack) {
		t.Fatalf("SetRecords() returned %d, want %d", len(reSet), len(readBack))
	}

	// ---- Phase 5: GetRecords - read again and verify match with first read ----
	t.Log("Phase 5: GetRecords - verify second read matches first")
	afterReSet, err := p.GetRecords(ctx, zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	readBack2 := collectRoundTripRecords(afterReSet)
	if len(readBack2) != len(readBack) {
		t.Fatalf("second read found %d records, want %d", len(readBack2), len(readBack))
	}
	if !recordsMatch(readBack, readBack2) {
		t.Error("records differ between first and second read")
		printRecordDiff(readBack, readBack2)
	}

	// ---- Phase 6: DeleteRecords - restore original state ----
	t.Log("Phase 6: DeleteRecords - restore original state")
	deleted2, err := p.DeleteRecords(ctx, zone, readBack2)
	if err != nil {
		t.Fatalf("DeleteRecords() error = %v", err)
	}
	if len(deleted2) != len(readBack2) {
		t.Fatalf("DeleteRecords() returned %d, want %d", len(deleted2), len(readBack2))
	}

	// Final verification: no test records left.
	final, err := p.GetRecords(ctx, zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	if leftover := collectRoundTripRecords(final); len(leftover) > 0 {
		t.Errorf("%d round-trip records still present after final delete", len(leftover))
	}
}

// collectRoundTripRecords returns all records whose last dot-separated name
// component starts with the round-trip test prefix (testPrefix + "-rt-").
// This handles both plain names (e.g. "_libdnstest-rt-a") and SRV names
// (e.g. "_sip._tcp._libdnstest-rt-srv").
func collectRoundTripRecords(all []libdns.Record) []libdns.Record {
	prefix := testPrefix + "-rt-"
	var result []libdns.Record
	for _, rec := range all {
		name := rec.RR().Name
		parts := strings.Split(name, ".")
		if strings.HasPrefix(parts[len(parts)-1], prefix) {
			result = append(result, rec)
		}
	}
	return result
}
