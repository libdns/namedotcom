// Implements the libdns interfaces for name.com
// https://www.name.com/api-docs
package namedotcom

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
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
		From     int32              `json:"from,omitempty"`
		To       int32              `json:"to,omitempty"`
	}

	// listRecordsResponse contains the response for the ListRecords function.
	listRecordsResponse struct {
		Records  []nameDotComRecord `json:"records,omitempty"`
		NextPage int32              `json:"nextPage,omitempty"`
		LastPage int32              `json:"lastPage,omitempty"`
		From     int32              `json:"from,omitempty"`
		To       int32              `json:"to,omitempty"`
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
		Priority   int32  `json:"priority,omitempty"`
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

	return er
}

// doRequest is the base http request handler including a request context.
func (n *nameDotCom) doRequest(ctx context.Context, method, endpoint string, post io.Reader) (io.Reader, error) {
	uri := n.Server + endpoint
	req, err := http.NewRequestWithContext(ctx, method, uri, post) // the offical name.com go client does not implement ctx
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(n.User, n.Token)
	if post != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return nil, n.errorResponse(resp)
	}

	return resp.Body, nil
}

// fromLibDNSRecord maps a libdns record to a name.com record. The id is the
// server-assigned record id for updates; zero for creates.
func (n *nameDotComRecord) fromLibDNSRecord(id int32, record libdns.Record, zone string) {
	rr := record.RR()

	n.ID = id
	n.Type = strings.ToUpper(rr.Type)
	n.Host = sanitizeHost(rr.Name, zone)
	n.TTL = uint32(rr.TTL.Seconds())

	switch n.Type {
	case "SRV", "MX":
		// libdns serializes priority first in the data field ("priority weight port target"
		// for SRV, "priority target" for MX), but name.com stores the priority in its own
		// field and expects the remaining fields in the answer.
		fields := strings.Fields(rr.Data)
		if len(fields) > 1 {
			priority, err := strconv.Atoi(fields[0])
			if err == nil {
				n.Priority = int32(priority)
			}
			n.Answer = strings.Join(fields[1:], " ")
		} else {
			n.Answer = rr.Data
		}
	default:
		n.Answer = rr.Data
	}
}

// toLibDNSRecord maps a name.com record to the libdns record type matching its
// RR type. Record types that libdns does not model (e.g. ANAME) are returned
// as opaque [libdns.RR] values.
func (r *nameDotComRecord) toLibDNSRecord(zone string) (libdns.Record, error) {
	name := libdns.RelativeName(r.Fqdn, zone)
	ttl := time.Duration(r.TTL) * time.Second

	switch r.Type {
	case "A", "AAAA":
		ip, err := netip.ParseAddr(r.Answer)
		if err != nil {
			return libdns.Address{}, fmt.Errorf("invalid %s answer %q: %w", r.Type, r.Answer, err)
		}
		return libdns.Address{
			Name: name,
			TTL:  ttl,
			IP:   ip,
		}, nil
	case "CNAME":
		return libdns.CNAME{
			Name:   name,
			TTL:    ttl,
			Target: r.Answer,
		}, nil
	case "MX":
		return libdns.MX{
			Name:       name,
			TTL:        ttl,
			Preference: uint16(r.Priority),
			Target:     r.Answer,
		}, nil
	case "NS":
		return libdns.NS{
			Name:   name,
			TTL:    ttl,
			Target: r.Answer,
		}, nil
	case "SRV":
		parts := strings.SplitN(name, ".", 3)
		if len(parts) < 2 {
			return libdns.SRV{}, fmt.Errorf("srv name %q does not contain enough fields; expected format: '_service._proto'", name)
		}

		contentParts := strings.Fields(r.Answer)
		if len(contentParts) < 3 {
			return libdns.SRV{}, fmt.Errorf("invalid srv answer %q; expected format: 'weight port target'", r.Answer)
		}
		weight, err := strconv.Atoi(contentParts[0])
		if err != nil {
			return libdns.SRV{}, fmt.Errorf("invalid value for weight %q: %w", contentParts[0], err)
		}
		port, err := strconv.Atoi(contentParts[1])
		if err != nil {
			return libdns.SRV{}, fmt.Errorf("invalid value for port %q: %w", contentParts[1], err)
		}

		srvName := "@"
		if len(parts) == 3 {
			srvName = parts[2]
		}

		return libdns.SRV{
			Service:   strings.TrimPrefix(parts[0], "_"),
			Transport: strings.TrimPrefix(parts[1], "_"),
			Name:      srvName,
			TTL:       ttl,
			Priority:  uint16(r.Priority),
			Weight:    uint16(weight),
			Port:      uint16(port),
			Target:    contentParts[2],
		}, nil
	case "TXT":
		return libdns.TXT{
			Name: name,
			TTL:  ttl,
			Text: r.Answer,
		}, nil
	default:
		// Record types that libdns does not model (e.g. ANAME) are returned
		// as opaque RRs so callers can still see them in the zone.
		return libdns.RR{
			Name: name,
			TTL:  ttl,
			Type: r.Type,
			Data: r.Answer,
		}, nil
	}
}

// sanitizeHost converts a libdns record name to the host value expected by
// name.com's api: relative to the zone with no trailing period. e.g. "sub.zone." -> "sub"
func sanitizeHost(name, zone string) string {
	return strings.TrimSuffix(strings.Replace(name, zone, "", -1), ".")
}

// NewNameDotComClient returns a new name.com client struct
func NewNameDotComClient(ctx context.Context, token, user, server string) (*nameDotCom, error) {
	if !strings.HasPrefix(server, "https://") && !strings.HasPrefix(server, "http://") {
		return nil, fmt.Errorf("invalid url %q, expecting http:// or https:// prefix", server)
	}

	httpClient := &http.Client{Timeout: HTTP_TIMEOUT * time.Second}

	return &nameDotCom{
		server, user, token,
		httpClient,
	}, nil
}
