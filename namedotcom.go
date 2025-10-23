// Implements the libdns interfaces for name.com
// https://www.name.com/api-docs
package namedotcom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/libdns/libdns"
)

// default timeout for the http request handler (seconds)
const HTTP_TIMEOUT = 30

type (
	nameDotCom struct {
		Server string `json:"server,omitempty"`
		User   string `json:"user,omitempty"`
		Token  string `json:"token,omitempty"`
		client *http.Client
	}

	listDomainsResponse struct {
		Domains  []nameDotComDomain `json:"domains,omitempty"`
		NextPage int32              `json:"nextPage,omitempty"`
		LastPage int32              `json:"lastPage,omitempty"`
	}

	// listRecordsResponse contains the response for the ListRecords function.
	listRecordsResponse struct {
		Records  []nameDotComRecord `json:"records,omitempty"`
		NextPage int32              `json:"nextPage,omitempty"`
		LastPage int32              `json:"lastPage,omitempty"`
	}

	// nameDotComRecord is an individual DNS resource record for name.com.
	nameDotComRecord struct {
		ID         int32  `json:"id,omitempty"`
		DomainName string `json:"domainName,omitempty"`
		Host       string `json:"host,omitempty"`
		Fqdn       string `json:"fqdn,omitempty"`
		Type       string `json:"type,omitempty"`
		Answer     string `json:"answer,omitempty"`
		TTL        uint32 `json:"ttl,omitempty"`
		Priority   uint32 `json:"priority,omitempty"`
	}

	nameDotComDomain struct {
		DomainName string `json:"domainName,omitempty"`
	}
)

type (
	// errorResponse is what is returned if the HTTP status code is not 200.
	errorResponse struct {
		// Message is the error message.
		Message string `json:"message,omitempty"`
		// Details may have some additional details about the error.
		Details string `json:"details,omitempty"`
	}
)

func (er errorResponse) Error() string {
	return er.Message + ": " + er.Details
}

// errorResponse - provides a more verbose stderr
func (n *nameDotCom) errorResponse(resp *http.Response) error {
	er := &errorResponse{}
	err := json.NewDecoder(resp.Body).Decode(er)
	if err != nil {
		return fmt.Errorf("api returned unexpected response: %w", err)
	}

	return err
}

// doRequest is the base http request handler including a request context.
func (n *nameDotCom) doRequest(ctx context.Context, method, endpoint string, post io.Reader) (io.Reader, error) {
	uri := n.Server + endpoint
	req, err := http.NewRequestWithContext(ctx, method, uri, post) // the offical name.com go client does not implement ctx
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(n.User, n.Token)
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, n.errorResponse(resp)
	}

	return resp.Body, nil
}

// fromLibDNSRecord maps a name.com record from a libdns record
func (n *nameDotComRecord) fromLibDNSRecord(id int32, record libdns.Record, zone string) {
	n.ID = id
	n.Type = record.RR().Type
	n.Host = n.toSanitized(record, zone)
	n.Answer = record.RR().Data
	n.TTL = uint32(record.RR().TTL.Seconds())
}

// toLibDNSRecord maps a name.com record to a libdns record
func (record *nameDotComRecord) toLibDNSRecord(zone string) (libdns.Record, error) {
	name := libdns.RelativeName(record.Fqdn, zone)
	ttl := time.Duration(record.TTL) * time.Second

	switch record.Type {
	case "A", "AAAA":
		ip, err := netip.ParseAddr(record.Answer)
		if err != nil {
			return libdns.Address{}, err
		}
		return libdns.Address{
			Name: name,
			TTL:  ttl,
			IP:   ip,
		}, nil
	case "CAA":
		contentParts := strings.SplitN(record.Answer, " ", 3)
		flags, err := strconv.Atoi(contentParts[0])
		if err != nil {
			return libdns.CAA{}, err
		}
		tag := contentParts[1]
		value := contentParts[2]
		return libdns.CAA{
			Name:  name,
			TTL:   ttl,
			Flags: uint8(flags),
			Tag:   tag,
			Value: value,
		}, nil
	case "CNAME":
		return libdns.CNAME{
			Name:   name,
			TTL:    ttl,
			Target: record.Answer,
		}, nil
	case "SRV":
		priority := record.Priority

		nameParts := strings.SplitN(name, ".", 2)
		if len(nameParts) < 2 {
			return libdns.SRV{}, fmt.Errorf("name %v does not contain enough fields; expected format: '_service._proto'", name)
		}
		contentParts := strings.SplitN(record.Answer, " ", 3)
		if len(contentParts) < 3 {
			return libdns.SRV{}, fmt.Errorf("content %v does not contain enough fields; expected format: 'weight port target'", name)
		}
		weight, err := strconv.Atoi(contentParts[0])
		if err != nil {
			return libdns.SRV{}, fmt.Errorf("invalid value for weight %v; expected integer", record.Priority)
		}
		port, err := strconv.Atoi(contentParts[1])
		if err != nil {
			return libdns.SRV{}, fmt.Errorf("invalid value for port %v; expected integer", record.Priority)
		}

		return libdns.SRV{
			Service:   strings.TrimPrefix(nameParts[0], "_"),
			Transport: strings.TrimPrefix(nameParts[1], "_"),
			Name:      zone,
			TTL:       ttl,
			Priority:  uint16(priority),
			Weight:    uint16(weight),
			Port:      uint16(port),
			Target:    contentParts[2],
		}, nil
	case "TXT":
		return libdns.TXT{
			Name: name,
			TTL:  ttl,
			Text: record.Answer,
		}, nil
	default:
		return libdns.RR{}, fmt.Errorf("Unsupported record type: %s", record.Type)
	}
}

// name.com's api server expects the sub domain name to be relavtive and have no trailing period
// , e.g. "sub.zone." -> "sub"
func (n *nameDotComRecord) toSanitized(libdnsRecord libdns.Record, zone string) string {
	return strings.TrimSuffix(strings.Replace(libdnsRecord.RR().Name, zone, "", -1), ".")
}

// NewNameDotComClient returns a new name.com client struct
func NewNameDotComClient(ctx context.Context, token, user, server string) (*nameDotCom, error) {
	re := regexp.MustCompile(`^https://.+\.com$`)
	validURL := re.MatchString(server)
	if !validURL {
		return nil, errors.New("invalid url scheme, expecting https:// prefix")
	}

	httpClient := &http.Client{Timeout: HTTP_TIMEOUT * time.Second}

	return &nameDotCom{
		server, user, token,
		httpClient,
	}, nil
}
