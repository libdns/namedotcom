package namedotcom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/libdns/libdns"
)

// nameClient extends the namedotcom api and request handler to the provider.
type nameClient struct {
	client *nameDotCom
	mutex  sync.Mutex
}

// getClient initiates a new nameClient and assigns it to the provider.
// A client set directly on the provider (e.g. for testing) is kept as-is.
func (p *Provider) getClient(ctx context.Context) error {
	if p.client != nil {
		return nil
	}

	newNameClient, err := NewNameDotComClient(ctx, p.Token, p.User, p.Server)
	if err != nil {
		return err
	}
	p.client = newNameClient
	return nil
}

// listZones returns all the zones (domains) for the user.
// GET /core/v1/domains
func (p *Provider) listZones(ctx context.Context) ([]libdns.Zone, error) {
	var (
		zones   []libdns.Zone
		body    io.Reader
		resp    = &listDomainsResponse{}
		reqPage = 1
		err     error
	)

	if err = p.getClient(ctx); err != nil {
		return []libdns.Zone{}, err
	}

	for reqPage > 0 {
		endpoint := fmt.Sprintf("/core/v1/domains?page=%d", reqPage)

		if body, err = p.client.doRequest(ctx, "GET", endpoint, nil); err != nil {
			return []libdns.Zone{}, fmt.Errorf("request failed: %w", err)
		}

		if err = json.NewDecoder(body).Decode(resp); err != nil {
			return []libdns.Zone{}, fmt.Errorf("could not decode name.com's response: %w", err)
		}

		for _, domain := range resp.Domains {
			zones = append(zones, libdns.Zone{
				Name: domain.DomainName,
			})
		}

		reqPage = int(resp.NextPage)
	}

	return zones, nil
}

// listAllRecords returns all the raw records for the given zone.
// GET /core/v1/domains/{domainName}/records
func (p *Provider) listAllRecords(ctx context.Context, zone string) ([]nameDotComRecord, error) {
	var (
		records []nameDotComRecord

		/*** 'zone' args that are passed in using compliant zone formats have the FQDN '.' suffix qualifier
		and in order to use the zone arg as a domainName reference to name.com's api we must remove the '.' suffix.
		otherwise the api will not recognize the domain. ***/
		unFQDNzone = strings.TrimSuffix(zone, ".")

		body    io.Reader
		resp    = &listRecordsResponse{}
		reqPage = 1

		err error
	)

	if err = p.getClient(ctx); err != nil {
		return []nameDotComRecord{}, err
	}

	// handle pagination, in case the domain has more records than the default page size
	for reqPage > 0 {
		endpoint := fmt.Sprintf("/core/v1/domains/%s/records?page=%d", unFQDNzone, reqPage)

		if body, err = p.client.doRequest(ctx, "GET", endpoint, nil); err != nil {
			return []nameDotComRecord{}, fmt.Errorf("request failed: %w", err)
		}

		if err = json.NewDecoder(body).Decode(resp); err != nil {
			return []nameDotComRecord{}, fmt.Errorf("could not decode name.com's response: %w", err)
		}

		records = append(records, resp.Records...)

		reqPage = int(resp.NextPage)
	}

	return records, nil
}

// findRecord returns the raw zone record that exactly matches the given
// libdns record (name, type and data). It returns a zero value with
// errRecordNotFound when no record matches.
func (p *Provider) findRecord(ctx context.Context, zone string, record libdns.Record) (nameDotComRecord, error) {
	records, err := p.listAllRecords(ctx, zone)
	if err != nil {
		return nameDotComRecord{}, err
	}

	for _, rec := range records {
		if sameName(rec, record.RR(), zone) &&
			strings.EqualFold(rec.Type, record.RR().Type) &&
			sameData(rec, record.RR()) {
			return rec, nil
		}
	}

	return nameDotComRecord{}, fmt.Errorf("could not find record with name %s: %w", record.RR().Name, errRecordNotFound)
}
