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
	Server: "https://api.name.com", // full url scheme expected here..
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

The test suite runs entirely in-process against a mock of the name.com CORE
API (see `mock_test.go`) seeded with sanitized record fixtures (`testdata/`),
so no credentials or network access are required:

```sh
go test ./...
```
