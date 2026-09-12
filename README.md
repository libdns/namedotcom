name.com for [`libdns`](https://github.com/libdns/libdns)
=======================

[![Go Reference](https://pkg.go.dev/badge/test.svg)](https://pkg.go.dev/github.com/libdns/namedotcom)

This package implements the [libdns interfaces](https://github.com/libdns/libdns) for name.com, allowing you to manage DNS records.

It talks to the current name.com CORE API (`/core/v1`) and targets `libdns` v1.x:
records are returned as the concrete libdns types (`Address`, `CNAME`, `MX`,
`NS`, `SRV`, `TXT`). Record types that libdns does not model (e.g. `ANAME`)
are returned as opaque [`libdns.RR`](https://pkg.go.dev/github.com/libdns/libdns#RR) values.

## Authenticating
To initiate the provider you need to supply the following parameters:
```go
provider := namedotcom.Provider{
	Token:  "NAMEDOTCOM_API_TOKEN",
	User:   "NAMEDOTCOM_USER_NAME",
	Server: "https://api.name.com", // or https://api.dev.name.com
}
```

## Example
Here's a basic example of how to list, update and delete records using this provider
```go
package main

import (
	"context"
	"log"
	"net/netip"
	"os"
	"time"

	"github.com/libdns/libdns"
	"github.com/libdns/namedotcom"
)

func main() {
	ctx := context.Background()

	zone := "example.com."

	// configure the name.com DNS provider
	provider := namedotcom.Provider{
		Token:  os.Getenv("NAMEDOTCOM_API_TOKEN"),
		User:   os.Getenv("NAMEDOTCOM_USER_NAME"),
		Server: os.Getenv("NAMEDOTCOM_SERVER"),
	}

	// list and iterate through all records
	recs, err := provider.GetRecords(ctx, zone)
	if err != nil {
		log.Fatal(err)
	}

	for _, rec := range recs {
		log.Println(rec)
	}

	// SetRecords creates or updates the given record. For each (name, type)
	// pair, only the provided records remain in the zone afterwards.
	newRecs, err := provider.SetRecords(ctx, zone, []libdns.Record{
		libdns.Address{
			Name: "sub",
			TTL:  300 * time.Second,
			IP:   netip.MustParseAddr("1.2.3.4"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(newRecs)

	// DeleteRecords removes the given record; records that do not exist in
	// the zone are silently ignored.
	deletedRecs, err := provider.DeleteRecords(ctx, zone, []libdns.Record{
		libdns.Address{
			Name: "sub",
			TTL:  300 * time.Second,
			IP:   netip.MustParseAddr("1.2.3.4"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(deletedRecs)
}
```

## Testing

The **same test suite** runs against either an in-process mock (default) or
the real name.com API. The distinguishing factor is the `NAMEDOTCOM_LIVE`
safety flag.

### Mock mode (default)

No credentials or network access required. An in-process mock server seeded
with sanitized fixtures (`testdata/`) is started for each test:

```sh
go test -v -count=1
```

### Live mode

Set `NAMEDOTCOM_LIVE=true` to opt in, plus credentials:

```sh
source ~/.config/namedotcom/<profile>.env
export NAMEDOTCOM_LIVE=true
go test -v -count=1
```

Optional overrides:

| Env var | Default | Purpose |
|---|---|---|
| `NAMEDOTCOM_BASE_URL` | `https://api.name.com` | API base URL (point at a mock or staging server) |
| `NAMEDOTCOM_TEST_ZONE` | first zone from `ListZones` | Zone to test against |

### Backup & integrity check

In live mode, `TestMain` snapshots the test zone to a timestamped TSV file
under `zone-backups/` before any test runs. After all tests complete, the
zone is re-read and compared against the snapshot. If any test left the zone
in a different state the run fails with a diff. The TSV files are never
deleted — they serve as an audit trail.

### Test organisation

All write tests use names prefixed with `_libdnstest-` so they cannot collide
with real records and are cleaned up via `t.Cleanup`.

- **Subdomain tests** (`TestAppendRecords`, `TestSetRecords`,
  `TestDeleteRecords`, `TestConcurrentAppend`, …) — operate on subdomain
  records only.
- **Apex tests** (`TestApex_*`) — operate on apex (`@`) records. These save
  and restore the original apex state. Test IPs use the `192.0.2.0/24`
  documentation range (RFC 5737) so they never route real traffic.
- **Mock-specific assertions** — `TestGetRecords` and `TestListZones` include
  extra checks against the fixture data that only run in mock mode.
