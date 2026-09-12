package namedotcom

// apex_test.go — write tests that operate on APEX records (name "@").
//
// These tests temporarily modify real apex records. They save the original
// apex state and restore it in t.Cleanup. In mock mode this is harmless
// (fresh mock per test). In live mode it is critical, and the TestMain
// integrity check provides a backstop.
//
// Test IPs use the 192.0.2.0/24 documentation range (RFC 5737) so they never
// route real traffic even if restore fails.

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// ---------------------------------------------------------------------------
// SetRecords — replace the apex A RRset
// ---------------------------------------------------------------------------

func TestApex_SetRecords_ReplaceA(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)

	// Save original apex A records so we can restore them.
	originals := saveApexRecordsByType(t, p, zone, "A")
	t.Logf("saved %d original apex A record(s):", len(originals))
	for _, rec := range originals {
		t.Logf("  %s", rec.RR().Data)
	}

	t.Cleanup(func() {
		restoreApexRRset(t, p, zone, originals, "A")
		restored := saveApexRecordsByType(t, p, zone, "A")
		t.Logf("restored %d apex A record(s) after test", len(restored))
	})

	// Replace apex A with test IPs from the 192.0.2.0/24 doc range.
	testRecords := []libdns.Record{
		libdns.Address{Name: "@", TTL: 300 * time.Second, IP: netip.MustParseAddr("192.0.2.50")},
		libdns.Address{Name: "@", TTL: 300 * time.Second, IP: netip.MustParseAddr("192.0.2.51")},
	}

	got, err := p.SetRecords(context.Background(), zone, testRecords)
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

	var apexA []libdns.Address
	for _, rec := range all {
		if addr, ok := rec.(libdns.Address); ok && addr.Name == "@" {
			apexA = append(apexA, addr)
		}
	}
	if len(apexA) != 2 {
		t.Fatalf("apex A records after set = %d, want exactly 2 (got %+v)", len(apexA), apexA)
	}
	ips := map[string]bool{apexA[0].IP.String(): true, apexA[1].IP.String(): true}
	if !ips["192.0.2.50"] || !ips["192.0.2.51"] {
		t.Errorf("apex A records = %v, want 192.0.2.50 and 192.0.2.51", apexA)
	}
	// Ensure the original IPs are gone.
	for _, a := range apexA {
		for _, orig := range originals {
			if a.IP.String() == orig.RR().Data {
				t.Errorf("old IP %s still present at apex after replace", a.IP)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// AppendRecords — append a TXT at the apex
// ---------------------------------------------------------------------------

func TestApex_AppendTXT(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)

	const testText = "_libdnstest apex txt"
	testTXT := libdns.TXT{
		Name: "@",
		TTL:  300 * time.Second,
		Text: testText,
	}

	t.Cleanup(func() {
		if _, err := p.DeleteRecords(context.Background(), zone, []libdns.Record{testTXT}); err != nil {
			t.Logf("cleanup: DeleteRecords() error: %v", err)
		}
	})

	got, err := p.AppendRecords(context.Background(), zone, []libdns.Record{testTXT})
	if err != nil {
		t.Fatalf("AppendRecords() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("AppendRecords() returned %d, want 1", len(got))
	}

	// Verify the TXT is visible among any existing apex TXT records.
	all, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	found := false
	for _, rec := range all {
		if txt, ok := rec.(libdns.TXT); ok && txt.Name == "@" && txt.Text == testText {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("appended apex TXT %q not found among %d records", testText, len(all))
	}
}

// ---------------------------------------------------------------------------
// DeleteRecords — delete a temporary apex A record
// ---------------------------------------------------------------------------

func TestApex_DeleteRecord(t *testing.T) {
	p := newTestProvider(t)
	zone := testZoneName(t, p)

	testIP := netip.MustParseAddr("192.0.2.99")
	testRec := libdns.Address{
		Name: "@",
		TTL:  300 * time.Second,
		IP:   testIP,
	}

	t.Cleanup(func() {
		// In case the delete test failed, clean up the test record.
		p.DeleteRecords(context.Background(), zone, []libdns.Record{testRec})
	})

	if _, err := p.AppendRecords(context.Background(), zone, []libdns.Record{testRec}); err != nil {
		t.Fatalf("AppendRecords() error = %v", err)
	}

	// Verify the test record exists.
	all, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	found := false
	for _, rec := range all {
		if addr, ok := rec.(libdns.Address); ok && addr.Name == "@" && addr.IP == testIP {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("test A record %s not found at apex after append", testIP)
	}

	// Delete it.
	got, err := p.DeleteRecords(context.Background(), zone, []libdns.Record{testRec})
	if err != nil {
		t.Fatalf("DeleteRecords() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("DeleteRecords() returned %d, want 1", len(got))
	}

	// Verify it's gone.
	all, err = p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("GetRecords() error = %v", err)
	}
	for _, rec := range all {
		if addr, ok := rec.(libdns.Address); ok && addr.Name == "@" && addr.IP == testIP {
			t.Errorf("test A record %s still present after DeleteRecords", testIP)
		}
	}
}
