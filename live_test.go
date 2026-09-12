package namedotcom

// Shared test infrastructure for running the same test suite against either
// an in-process mock (default) or a real/external name.com API.
//
// # Mock mode (default)
//
// No environment variables are required. An in-process mock server seeded
// with sanitized fixtures (testdata/) is started for each test.
//
//	go test -v -count=1
//
// # Live mode
//
// Set NAMEDOTCOM_LIVE=true to opt in, plus credentials:
//
//	export NAMEDOTCOM_LIVE=true
//	export NAMEDOTCOM_USERNAME=your-username
//	export NAMEDOTCOM_TOKEN=your-token
//	# optional overrides:
//	# export NAMEDOTCOM_BASE_URL=https://api.name.com
//	# export NAMEDOTCOM_TEST_ZONE=your-domain.com
//	go test -v -count=1
//
// In live mode TestMain snapshots the test zone to a timestamped TSV file
// (zone-backups/) before tests and verifies the zone matches after tests.
// The backup files are never deleted — they serve as an audit trail.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

const (
	testPrefix    = "_libdnstest"
	zoneBackupDir = "zone-backups"
)

// Package-level state for the TestMain backup/verify lifecycle.
var (
	liveBackupZone    string
	liveBackupFile    string
	liveBackupRecords []libdns.Record
)

// isLiveMode reports whether tests run against a real/external API.
// The safety flag NAMEDOTCOM_LIVE must be explicitly set to "true" to
// opt in; otherwise the in-process mock is used.
func isLiveMode() bool {
	return os.Getenv("NAMEDOTCOM_LIVE") == "true"
}

// liveServer returns the API base URL. It reads NAMEDOTCOM_BASE_URL when
// in live mode, defaulting to the production name.com API.
func liveServer() string {
	if u := os.Getenv("NAMEDOTCOM_BASE_URL"); u != "" {
		return u
	}
	return "https://api.name.com"
}

// -----------------------------------------------------------------------
// TestMain — backup before, verify after
// -----------------------------------------------------------------------

func TestMain(m *testing.M) {
	if isLiveMode() {
		if os.Getenv("NAMEDOTCOM_TEST_ZONE") == "" {
			fmt.Fprintln(os.Stderr, "ERROR: NAMEDOTCOM_LIVE=true requires NAMEDOTCOM_TEST_ZONE to be set explicitly.")
			fmt.Fprintln(os.Stderr, "       This safety check prevents accidentally mutating an unknown zone.")
			os.Exit(1)
		}
		setupLiveBackup()
	}

	code := m.Run()

	if liveBackupZone != "" {
		code = verifyZoneIntegrity(code)
	}

	os.Exit(code)
}

// setupLiveBackup reads the test zone (from NAMEDOTCOM_TEST_ZONE, which
// TestMain has already verified is set) and writes a timestamped TSV snapshot.
// On any failure it logs a warning and leaves the backup state empty (tests
// still run, but without the integrity check).
func setupLiveBackup() {
	user := os.Getenv("NAMEDOTCOM_USERNAME")
	token := os.Getenv("NAMEDOTCOM_TOKEN")
	zone := os.Getenv("NAMEDOTCOM_TEST_ZONE")

	p := &Provider{
		Token:  token,
		User:   user,
		Server: liveServer(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	records, err := p.GetRecords(ctx, zone)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: could not read zone %s for backup: %v\n", zone, err)
		return
	}

	path, err := writeZoneBackup(zone, records, user)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: could not write zone backup: %v\n", err)
		return
	}

	liveBackupZone = zone
	liveBackupFile = path
	liveBackupRecords = records
	fmt.Fprintf(os.Stderr, "Zone backup: %s (%d records for %s)\n", path, len(records), zone)
}

// verifyZoneIntegrity re-reads the zone and compares it against the pre-run
// snapshot. Returns the original exit code on success, or 1 on mismatch.
func verifyZoneIntegrity(code int) int {
	p := &Provider{
		Token:  os.Getenv("NAMEDOTCOM_TOKEN"),
		User:   os.Getenv("NAMEDOTCOM_USERNAME"),
		Server: liveServer(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	current, err := p.GetRecords(ctx, liveBackupZone)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n*** ZONE INTEGRITY CHECK FAILED ***\n  could not re-read zone %s: %v\n", liveBackupZone, err)
		return 1
	}

	if !recordsMatch(liveBackupRecords, current) {
		fmt.Fprintf(os.Stderr, "\n*** ZONE INTEGRITY CHECK FAILED ***\n  zone %s does not match backup %s\n  backup: %d records, current: %d records\n",
			liveBackupZone, liveBackupFile, len(liveBackupRecords), len(current))
		printRecordDiff(liveBackupRecords, current)
		return 1
	}

	fmt.Fprintf(os.Stderr, "Zone integrity OK: %s matches backup (%d records)\n", liveBackupZone, len(current))
	return code
}

// -----------------------------------------------------------------------
// TSV backup helpers
// -----------------------------------------------------------------------

// writeZoneBackup writes records to a timestamped TSV file in zoneBackupDir.
// The file is left on disk as an audit trail.
func writeZoneBackup(zone string, records []libdns.Record, account string) (string, error) {
	if err := os.MkdirAll(zoneBackupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	safeZone := strings.ReplaceAll(zone, ".", "_")
	filename := fmt.Sprintf("%s_%s.tsv", safeZone, ts)
	path := filepath.Join(zoneBackupDir, filename)

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create backup file: %w", err)
	}
	defer f.Close()

	fmt.Fprintf(f, "# name.com zone backup\n")
	fmt.Fprintf(f, "# zone: %s\n", zone)
	fmt.Fprintf(f, "# account: %s\n", account)
	fmt.Fprintf(f, "# timestamp: %s\n", time.Now().UTC().Format(time.RFC3339Nano))
	fmt.Fprintf(f, "# record_count: %d\n", len(records))
	fmt.Fprintf(f, "type\tname\tttl\tdata\n")

	sorted := make([]libdns.Record, len(records))
	copy(sorted, records)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri, rj := sorted[i].RR(), sorted[j].RR()
		if ri.Type != rj.Type {
			return ri.Type < rj.Type
		}
		return ri.Name < rj.Name
	})

	for _, rec := range sorted {
		rr := rec.RR()
		fmt.Fprintf(f, "%s\t%s\t%d\t%s\n", rr.Type, rr.Name, rr.TTL/time.Second, rr.Data)
	}

	return path, nil
}

// recordKey returns a normalised comparison key for a record.
func recordKey(r libdns.RR) string {
	return fmt.Sprintf("%s\t%s\t%d\t%s",
		strings.ToUpper(r.Type),
		strings.ToLower(r.Name),
		r.TTL/time.Second,
		strings.TrimSuffix(r.Data, "."))
}

// recordsMatch reports whether two record sets contain the same records
// (order-independent).
func recordsMatch(a, b []libdns.Record) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, rec := range a {
		counts[recordKey(rec.RR())]++
	}
	for _, rec := range b {
		k := recordKey(rec.RR())
		counts[k]--
		if counts[k] < 0 {
			return false
		}
	}
	return true
}

// printRecordDiff prints the per-key differences between two record sets.
func printRecordDiff(backup, current []libdns.Record) {
	backupSet := make(map[string]int)
	for _, rec := range backup {
		backupSet[recordKey(rec.RR())]++
	}
	currentSet := make(map[string]int)
	for _, rec := range current {
		currentSet[recordKey(rec.RR())]++
	}

	allKeys := make(map[string]bool)
	for k := range backupSet {
		allKeys[k] = true
	}
	for k := range currentSet {
		allKeys[k] = true
	}
	var keys []string
	for k := range allKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintf(os.Stderr, "  Diff (backup → current):\n")
	for _, k := range keys {
		b := backupSet[k]
		c := currentSet[k]
		if b != c {
			fmt.Fprintf(os.Stderr, "    [%s] %d → %d\n", k, b, c)
		}
	}
}

// -----------------------------------------------------------------------
// Shared test helpers (used by tests in provider_test.go and apex_test.go)
// -----------------------------------------------------------------------

// liveTestZone returns the zone to use for live tests. TestMain has already
// verified that NAMEDOTCOM_TEST_ZONE is set, so this always returns a valid
// zone.
func liveTestZone(t *testing.T, p *Provider) string {
	t.Helper()
	if liveBackupZone != "" {
		return liveBackupZone
	}
	// TestMain should have caught this, but guard anyway.
	if z := os.Getenv("NAMEDOTCOM_TEST_ZONE"); z != "" {
		return z
	}
	t.Fatal("NAMEDOTCOM_TEST_ZONE is required in live mode")
	return ""
}

// cleanupTestRecords deletes every record in the zone whose name starts with
// testPrefix. Intended for t.Cleanup in write tests.
func cleanupTestRecords(t *testing.T, p *Provider, zone string) {
	t.Helper()
	ctx := context.Background()
	records, err := p.GetRecords(ctx, zone)
	if err != nil {
		t.Logf("cleanup: GetRecords error: %v", err)
		return
	}
	var toDelete []libdns.Record
	for _, rec := range records {
		// For most records the name is a plain label (e.g.
		// "_libdnstest-append-a"). For SRV records the name is
		// "_service._proto.label" (e.g. "_sip._tcp._libdnstest-srv"),
		// so we check the last dot-separated component.
		name := rec.RR().Name
		parts := strings.Split(name, ".")
		if strings.HasPrefix(parts[len(parts)-1], testPrefix) {
			toDelete = append(toDelete, rec)
		}
	}
	if len(toDelete) > 0 {
		t.Logf("cleanup: deleting %d leftover test records", len(toDelete))
		if _, err := p.DeleteRecords(ctx, zone, toDelete); err != nil {
			t.Logf("cleanup: DeleteRecords error: %v", err)
		}
	}
}

// findRecordOpt returns the first record matching name and type, or nil if
// not found.
func findRecordOpt(recs []libdns.Record, name, typ string) libdns.Record {
	for _, rec := range recs {
		rr := rec.RR()
		if strings.EqualFold(rr.Name, name) && rr.Type == typ {
			return rec
		}
	}
	return nil
}

// -----------------------------------------------------------------------
// Apex test helpers
// ---------------------------------------------------------------------------

// saveApexRecordsByType returns all apex ("@") records matching the given
// RR type from the zone.
func saveApexRecordsByType(t *testing.T, p *Provider, zone, typ string) []libdns.Record {
	t.Helper()
	all, err := p.GetRecords(context.Background(), zone)
	if err != nil {
		t.Fatalf("saveApexRecords: GetRecords() error = %v", err)
	}
	var result []libdns.Record
	for _, rec := range all {
		rr := rec.RR()
		if rr.Name == "@" && rr.Type == typ {
			result = append(result, rec)
		}
	}
	return result
}

// restoreApexRRset restores the given original records at the apex by calling
// SetRecords, which replaces the entire (name, type) RRset. If originals is
// empty, any current apex records of the same type are deleted instead.
func restoreApexRRset(t *testing.T, p *Provider, zone string, originals []libdns.Record, typ string) {
	t.Helper()
	ctx := context.Background()
	if len(originals) > 0 {
		if _, err := p.SetRecords(ctx, zone, originals); err != nil {
			t.Errorf("restore: SetRecords() error: %v", err)
		}
		return
	}
	all, err := p.GetRecords(ctx, zone)
	if err != nil {
		t.Logf("restore: GetRecords() error: %v", err)
		return
	}
	var toDelete []libdns.Record
	for _, rec := range all {
		rr := rec.RR()
		if rr.Name == "@" && rr.Type == typ {
			toDelete = append(toDelete, rec)
		}
	}
	if len(toDelete) > 0 {
		if _, err := p.DeleteRecords(ctx, zone, toDelete); err != nil {
			t.Logf("restore: DeleteRecords() error: %v", err)
		}
	}
}
