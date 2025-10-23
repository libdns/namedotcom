package namedotcom

import (
	"bytes"
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

// getClient initiates a new nameClient and assigns it to the provider..
func (p *Provider) getClient(ctx context.Context) error {
	newNameClient, err := NewNameDotComClient(ctx, p.Token, p.User, p.Server)
	if err != nil {
		return err
	}
	p.client = newNameClient
	return nil
}

// listZones returns all the zones (domains) for the user
func (p *Provider) listZones(ctx context.Context) ([]libdns.Zone, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var (
		zones   []libdns.Zone
		method  = "GET"
		body    io.Reader
		resp    = &listDomainsResponse{}
		reqPage = 1
		err     error
	)

	if err = p.getClient(ctx); err != nil {
		return []libdns.Zone{}, err
	}

	for reqPage > 0 {
		if reqPage != 0 {
			endpoint := fmt.Sprintf("/v4/domains?page=%d", reqPage)

			if body, err = p.client.doRequest(ctx, method, endpoint, nil); err != nil {
				return []libdns.Zone{}, fmt.Errorf("request failed:  %w", err)
			}

			if err = json.NewDecoder(body).Decode(resp); err != nil {
				return []libdns.Zone{}, fmt.Errorf("could not decode name.com's response:  %w", err)
			}

			for _, domain := range resp.Domains {
				zones = append(zones, libdns.Zone{
					Name: domain.DomainName,
				})
			}

			reqPage = int(resp.NextPage)
		}
	}

	return zones, nil
}

func (p *Provider) getRecordId(ctx context.Context, zone string, record libdns.Record) (int32, error) {
	records, err := p.listAllRecords(ctx, zone)
	if err != nil {
		return 0, err
	}

	name := libdns.AbsoluteName(record.RR().Name, zone)
	for _, rec := range records {
		if rec.Type == record.RR().Type && rec.Fqdn == name && rec.Answer == record.RR().Data {
			return rec.ID, nil
		}
	}

	return 0, fmt.Errorf("could not find record with name %s", record.RR().Name)
}

func (p *Provider) listAllRecordsLocked(ctx context.Context, zone string) ([]nameDotComRecord, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	return p.listAllRecords(ctx, zone)
}

// listAllRecords returns all records for the given zone . GET /v4/domains/{ domainName }/records
func (p *Provider) listAllRecords(ctx context.Context, zone string) ([]nameDotComRecord, error) {
	var (
		records []nameDotComRecord

		/*** 'zone' args that are passed in using compliant zone formats have the FQDN '.' suffix qualifier
		and in order to use the zone arg as a domainName reference to name.com's api we must remove the '.' suffix.
		otherwise the api will not recognize the domain.. ***/
		unFQDNzone = strings.TrimSuffix(zone, ".")

		method  = "GET"
		body    io.Reader
		resp    = &listRecordsResponse{}
		reqPage = 1

		err error
	)

	if err = p.getClient(ctx); err != nil {
		return []nameDotComRecord{}, err
	}

	// handle pagination, in case domain has more records than the default of 1000 per page
	for reqPage > 0 {
		if reqPage != 0 {
			endpoint := fmt.Sprintf("/v4/domains/%s/records?page=%d", unFQDNzone, reqPage)

			if body, err = p.client.doRequest(ctx, method, endpoint, nil); err != nil {
				return []nameDotComRecord{}, fmt.Errorf("request failed:  %w", err)
			}

			if err = json.NewDecoder(body).Decode(resp); err != nil {
				return []nameDotComRecord{}, fmt.Errorf("could not decode name.com's response:  %w", err)
			}

			for _, record := range resp.Records {
				records = append(records, record)
			}

			reqPage = int(resp.NextPage)
		}
	}

	return records, nil
}

// deleteRecord  DELETE /v4/domains/{ domainName }/records/{ record.ID }
func (p *Provider) deleteRecord(ctx context.Context, zone string, record libdns.Record) (libdns.Record, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	recordId, err := p.getRecordId(ctx, zone, record)
	if err != nil {
		return libdns.RR{}, err
	}

	var (
		shouldDelete nameDotComRecord
		unFQDNzone   = strings.TrimSuffix(zone, ".")

		method   = "DELETE"
		endpoint = fmt.Sprintf("/v4/domains/%s/records/%d", unFQDNzone, recordId)
		body     io.Reader
		post     = &bytes.Buffer{}
	)

	shouldDelete.fromLibDNSRecord(recordId, record, unFQDNzone)

	if err = p.getClient(ctx); err != nil {
		return libdns.RR{}, err
	}

	if err = json.NewEncoder(post).Encode(shouldDelete); err != nil {
		return libdns.RR{}, fmt.Errorf("could not encode form data for request:  %w", err)
	}

	if body, err = p.client.doRequest(ctx, method, endpoint, post); err != nil {
		return libdns.RR{}, fmt.Errorf("request to delete the record was not successful:  %w", err)
	}

	if err = json.NewDecoder(body).Decode(&shouldDelete); err != nil {
		return libdns.RR{}, fmt.Errorf("could not decode the response from name.com:  %w", err)
	}

	return shouldDelete.toLibDNSRecord(unFQDNzone)
}

// upsertRecord  PUT || POST /v4/domains/{ domainName }/records/{ record.ID }
func (p *Provider) upsertRecord(ctx context.Context, zone string, canidateRecord libdns.Record) (libdns.Record, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	recordId, err := p.getRecordId(ctx, zone, canidateRecord)

	var (
		shouldUpsert nameDotComRecord
		unFQDNzone   = strings.TrimSuffix(zone, ".")

		method   = "PUT"
		endpoint = fmt.Sprintf("/v4/domains/%s/records/%d", unFQDNzone, recordId)
		body     io.Reader
		post     = &bytes.Buffer{}
	)

	if err != nil {
		method = "POST"
		endpoint = fmt.Sprintf("/v4/domains/%s/records", unFQDNzone)
	}

	shouldUpsert.fromLibDNSRecord(recordId, canidateRecord, unFQDNzone)

	if err = p.getClient(ctx); err != nil {
		return libdns.RR{}, err
	}

	if err = json.NewEncoder(post).Encode(shouldUpsert); err != nil {
		return libdns.RR{}, fmt.Errorf("could not encode the form data for the request:  %w", err)
	}

	if body, err = p.client.doRequest(ctx, method, endpoint, post); err != nil {
		if strings.Contains(err.Error(), "Duplicate Record") {
			err = fmt.Errorf("name.com will not allow an update to a record that has identical values to an existing record: %w", err)
		}

		return libdns.RR{}, fmt.Errorf("request to update the record was not successful:  %w", err)
	}

	if err = json.NewDecoder(body).Decode(&shouldUpsert); err != nil {
		return libdns.RR{}, fmt.Errorf("could not decode name.com's response:  %w", err)
	}

	return shouldUpsert.toLibDNSRecord(unFQDNzone)
}
