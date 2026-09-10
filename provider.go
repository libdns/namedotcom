// namedotcom implements the libdns interfaces to manage name.com dns records

package namedotcom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/libdns/libdns"
)

// errRecordNotFound is returned by findRecord when no record in the zone
// matches the given name, type and data.
var errRecordNotFound = errors.New("record not found")

// Provider implements the libdns interface for namedotcom
type Provider struct {
	nameClient
	Token  string `json:"api_token,omitempty"`
	User   string `json:"user,omitempty"`
	Server string `json:"server,omitempty"` // e.g. https://api.name.com or https://api.dev.name.com
}

// GetRecords lists all the records in the zone.
func (p *Provider) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	records, err := p.listAllRecords(ctx, zone)
	if err != nil {
		return nil, err
	}

	var result []libdns.Record
	for _, record := range records {
		rec, err := record.toLibDNSRecord(zone)
		if err != nil {
			return []libdns.Record{}, fmt.Errorf("could not decode name.com's response: %w", err)
		}
		result = append(result, rec)
	}

	return result, nil
}

// AppendRecords adds records to the zone. It returns the records that were added.
// It never changes existing records; adding a record that already exists in the
// zone is an error.
func (p *Provider) AppendRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var appendedRecords []libdns.Record

	for _, record := range records {
		newRecord, err := p.createRecord(ctx, zone, record)
		if err != nil {
			return nil, err
		}
		appendedRecords = append(appendedRecords, newRecord)
	}

	return appendedRecords, nil
}

// SetRecords sets the records in the zone, either by updating existing records or creating new ones.
// It returns the updated records.
//
// For each (name, type) pair in the input, only the provided records remain in
// the zone afterwards: one existing record is updated per input record, extra
// input records are created, and surplus existing records are deleted.
func (p *Provider) SetRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	zoneRecords, err := p.listAllRecords(ctx, zone)
	if err != nil {
		return nil, err
	}

	var setRecords []libdns.Record

	// Group the input by (name, type), preserving order, so each RRset is
	// reconciled against the zone as a whole.
	type rrsetKey struct{ name, typ string }
	groups := make(map[rrsetKey][]libdns.Record)
	var order []rrsetKey
	for _, record := range records {
		key := rrsetKey{name: strings.ToLower(record.RR().Name), typ: strings.ToUpper(record.RR().Type)}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], record)
	}

	for _, key := range order {
		group := groups[key]

		var existing []nameDotComRecord
		for _, rec := range zoneRecords {
			if strings.EqualFold(rec.Type, key.typ) && sameName(rec, group[0].RR(), zone) {
				existing = append(existing, rec)
			}
		}

		for i, record := range group {
			var (
				setRecord libdns.Record
				err       error
			)
			if i < len(existing) {
				setRecord, err = p.updateRecord(ctx, zone, existing[i].ID, record)
			} else {
				setRecord, err = p.createRecord(ctx, zone, record)
			}
			if err != nil {
				return setRecords, err
			}
			setRecords = append(setRecords, setRecord)
		}

		// Delete any surplus existing records so that only the input records
		// remain for this (name, type) pair.
		for j := len(group); j < len(existing); j++ {
			if _, err := p.removeRecord(ctx, zone, existing[j]); err != nil {
				return setRecords, err
			}
		}
	}

	return setRecords, nil
}

// DeleteRecords deletes the records from the zone. It returns the records that were deleted.
// Input records that do not exist in the zone are silently ignored.
func (p *Provider) DeleteRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var deletedRecords []libdns.Record

	for _, record := range records {
		found, err := p.findRecord(ctx, zone, record)
		if err != nil {
			if errors.Is(err, errRecordNotFound) {
				continue
			}
			return nil, err
		}

		deletedRecord, err := p.removeRecord(ctx, zone, found)
		if err != nil {
			return deletedRecords, err
		}
		deletedRecords = append(deletedRecords, deletedRecord)
	}

	return deletedRecords, nil
}

func (p *Provider) ListZones(ctx context.Context) ([]libdns.Zone, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	zones, err := p.listZones(ctx)
	if err != nil {
		return nil, err
	}

	return zones, nil
}

// createRecord creates a new record in the zone.
func (p *Provider) createRecord(ctx context.Context, zone string, record libdns.Record) (libdns.Record, error) {
	unFQDNzone := strings.TrimSuffix(zone, ".")

	var (
		shouldCreate nameDotComRecord
		body         io.Reader
		post         = &bytes.Buffer{}
		err          error
	)

	shouldCreate.fromLibDNSRecord(0, record, unFQDNzone)

	if err = p.getClient(ctx); err != nil {
		return libdns.RR{}, err
	}

	if err = json.NewEncoder(post).Encode(shouldCreate); err != nil {
		return libdns.RR{}, fmt.Errorf("could not encode the form data for the request: %w", err)
	}

	endpoint := fmt.Sprintf("/core/v1/domains/%s/records", unFQDNzone)
	if body, err = p.client.doRequest(ctx, "POST", endpoint, post); err != nil {
		return libdns.RR{}, fmt.Errorf("request to create the record was not successful: %w", err)
	}

	if err = json.NewDecoder(body).Decode(&shouldCreate); err != nil {
		return libdns.RR{}, fmt.Errorf("could not decode name.com's response: %w", err)
	}

	return shouldCreate.toLibDNSRecord(unFQDNzone)
}

// updateRecord updates an existing record in the zone by id.
func (p *Provider) updateRecord(ctx context.Context, zone string, id int32, record libdns.Record) (libdns.Record, error) {
	unFQDNzone := strings.TrimSuffix(zone, ".")

	var (
		shouldUpdate nameDotComRecord
		body         io.Reader
		post         = &bytes.Buffer{}
		err          error
	)

	shouldUpdate.fromLibDNSRecord(id, record, unFQDNzone)

	if err = p.getClient(ctx); err != nil {
		return libdns.RR{}, err
	}

	if err = json.NewEncoder(post).Encode(shouldUpdate); err != nil {
		return libdns.RR{}, fmt.Errorf("could not encode the form data for the request: %w", err)
	}

	endpoint := fmt.Sprintf("/core/v1/domains/%s/records/%d", unFQDNzone, id)
	if body, err = p.client.doRequest(ctx, "PUT", endpoint, post); err != nil {
		return libdns.RR{}, fmt.Errorf("request to update the record was not successful: %w", err)
	}

	if err = json.NewDecoder(body).Decode(&shouldUpdate); err != nil {
		return libdns.RR{}, fmt.Errorf("could not decode name.com's response: %w", err)
	}

	return shouldUpdate.toLibDNSRecord(unFQDNzone)
}

// removeRecord deletes a raw record from the zone by id.
func (p *Provider) removeRecord(ctx context.Context, zone string, record nameDotComRecord) (libdns.Record, error) {
	unFQDNzone := strings.TrimSuffix(zone, ".")

	if err := p.getClient(ctx); err != nil {
		return libdns.RR{}, err
	}

	endpoint := fmt.Sprintf("/core/v1/domains/%s/records/%d", unFQDNzone, record.ID)
	// name.com answers 204 No Content on success; there is no body to decode.
	if _, err := p.client.doRequest(ctx, "DELETE", endpoint, nil); err != nil {
		return libdns.RR{}, fmt.Errorf("request to delete the record was not successful: %w", err)
	}

	return record.toLibDNSRecord(unFQDNzone)
}

// sameName reports whether the name of a libdns record (relative to zone)
// matches the fqdn of a name.com record.
func sameName(rec nameDotComRecord, rr libdns.RR, zone string) bool {
	want := strings.TrimSuffix(libdns.AbsoluteName(rr.Name, zone), ".")
	have := strings.TrimSuffix(rec.Fqdn, ".")
	return strings.EqualFold(want, have)
}

// sameData reports whether the data of a libdns record matches the answer of
// a name.com record. For MX and SRV records the priority is stored in its own
// field by name.com, so it is excluded from the comparison. Hostname-like
// answers are compared without regard to a trailing dot, since name.com
// normalizes targets on write.
func sameData(rec nameDotComRecord, rr libdns.RR) bool {
	data := rr.Data

	switch rec.Type {
	case "SRV", "MX":
		fields := strings.Fields(data)
		if len(fields) > 1 {
			data = strings.Join(fields[1:], " ")
		}
	case "TXT":
		return data == rec.Answer
	default:
		return strings.EqualFold(strings.TrimSuffix(data, "."), strings.TrimSuffix(rec.Answer, "."))
	}

	return strings.EqualFold(strings.TrimSuffix(data, "."), strings.TrimSuffix(rec.Answer, "."))
}

// Interface guards
var (
	_ libdns.RecordGetter   = (*Provider)(nil)
	_ libdns.RecordAppender = (*Provider)(nil)
	_ libdns.RecordSetter   = (*Provider)(nil)
	_ libdns.RecordDeleter  = (*Provider)(nil)
	_ libdns.ZoneLister     = (*Provider)(nil)
)
