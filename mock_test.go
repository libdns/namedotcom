package namedotcom

// mock_test.go implements an in-process name.com CORE API (core/v1) test
// server. Its behavior mirrors the sanitized live captures in
// ~/Projects/curldns/fixtures/name.com/ (request/response shapes, status
// codes and error bodies), so the provider tests run without credentials or
// network access.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	mockUser     = "example-user"
	mockToken    = "example-token"
	mockZoneName = "example.com"
)

var mockRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "ANAME": true, "CNAME": true,
	"MX": true, "NS": true, "SRV": true, "TXT": true,
}

// mockNameCom is a stateful in-memory name.com CORE API.
type mockNameCom struct {
	mu      sync.Mutex
	domains []nameDotComDomain
	records []nameDotComRecord
	nextID  int32
}

func newMockNameCom(t *testing.T) *mockNameCom {
	t.Helper()

	domainsBody, err := os.ReadFile("testdata/domains.json")
	if err != nil {
		t.Fatalf("read testdata/domains.json: %v", err)
	}
	var domainsResp listDomainsResponse
	if err := json.Unmarshal(domainsBody, &domainsResp); err != nil {
		t.Fatalf("decode testdata/domains.json: %v", err)
	}

	recordsBody, err := os.ReadFile("testdata/records.json")
	if err != nil {
		t.Fatalf("read testdata/records.json: %v", err)
	}
	var recordsResp listRecordsResponse
	if err := json.Unmarshal(recordsBody, &recordsResp); err != nil {
		t.Fatalf("decode testdata/records.json: %v", err)
	}

	nextID := int32(200000)
	for _, rec := range recordsResp.Records {
		if rec.ID >= nextID {
			nextID = rec.ID + 1
		}
	}

	return &mockNameCom{
		domains: domainsResp.Domains,
		records: recordsResp.Records,
		nextID:  nextID,
	}
}

func (m *mockNameCom) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != mockUser || token != mockToken {
			m.json(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"})
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/core/v1/domains":
			m.handleListDomains(w)
		case strings.HasPrefix(r.URL.Path, "/core/v1/domains/"):
			m.handleDomain(w, r)
		default:
			m.json(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		}
	})
}

func (m *mockNameCom) handleListDomains(w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()

	body := struct {
		Domains    []nameDotComDomain `json:"domains"`
		From       int32              `json:"from"`
		To         int32              `json:"to"`
		TotalCount int32              `json:"totalCount"`
	}{m.domains, 1, int32(len(m.domains)), int32(len(m.domains))}

	m.json(w, http.StatusOK, body)
}

func (m *mockNameCom) handleDomain(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/core/v1/domains/"), "/")
	if len(parts) < 2 || parts[0] != mockZoneName {
		m.json(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}

	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		m.handleListRecords(w)
	case len(parts) == 2 && r.Method == http.MethodPost:
		m.handleCreateRecord(w, r)
	case len(parts) == 3 && r.Method == http.MethodPut:
		m.handleUpdateRecord(w, r, parts[2])
	case len(parts) == 3 && r.Method == http.MethodDelete:
		m.handleDeleteRecord(w, r, parts[2])
	default:
		m.json(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	}
}

func (m *mockNameCom) handleListRecords(w http.ResponseWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()

	resp := struct {
		TotalCount int32              `json:"totalCount"`
		From       int32              `json:"from"`
		To         int32              `json:"to"`
		Records    []nameDotComRecord `json:"records"`
	}{TotalCount: int32(len(m.records)), From: 1, To: int32(len(m.records)), Records: m.records}

	m.json(w, http.StatusOK, resp)
}

func (m *mockNameCom) handleCreateRecord(w http.ResponseWriter, r *http.Request) {
	var input nameDotComRecord
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		m.json(w, http.StatusBadRequest, map[string]string{"message": "Invalid request body"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !mockRecordTypes[input.Type] {
		m.json(w, http.StatusBadRequest, map[string]string{
			"message": fmt.Sprintf("Invalid value '%s' for 'type', must be one of 'A', 'AAAA', 'ANAME', 'CNAME', 'MX', 'NS', 'SRV', 'TXT'.", input.Type),
		})
		return
	}
	if input.Answer == "" {
		m.json(w, http.StatusBadRequest, map[string]string{"message": "'answer' can't be null"})
		return
	}

	record := m.normalize(input)
	for _, existing := range m.records {
		if strings.EqualFold(existing.Host, record.Host) &&
			strings.EqualFold(existing.Type, record.Type) &&
			existing.Answer == record.Answer {
			m.json(w, http.StatusBadRequest, map[string]string{"message": "Parameter Value Error - Record already exists"})
			return
		}
	}

	m.records = append(m.records, record)
	m.json(w, http.StatusOK, record)
}

func (m *mockNameCom) handleUpdateRecord(w http.ResponseWriter, r *http.Request, id string) {
	var input nameDotComRecord
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		m.json(w, http.StatusBadRequest, map[string]string{"message": "Invalid request body"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	idNum, err := strconv.Atoi(id)
	if err != nil {
		m.json(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}

	for i := range m.records {
		if m.records[i].ID == int32(idNum) {
			record := m.normalize(input)
			record.ID = int32(idNum)
			m.records[i] = record
			m.json(w, http.StatusOK, record)
			return
		}
	}

	m.json(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func (m *mockNameCom) handleDeleteRecord(w http.ResponseWriter, r *http.Request, id string) {
	idNum, err := strconv.Atoi(id)
	if err != nil {
		m.json(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.records {
		if m.records[i].ID == int32(idNum) {
			m.records = append(m.records[:i], m.records[i+1:]...)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	m.json(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

// normalize applies the same answer/host normalization the live API performs:
// apex hosts come back empty and hostname-like answers lose their trailing dot.
func (m *mockNameCom) normalize(input nameDotComRecord) nameDotComRecord {
	record := input
	if record.Host == "@" {
		record.Host = ""
	}
	switch record.Type {
	case "TXT":
		// text is stored verbatim
	default:
		record.Answer = strings.TrimSuffix(record.Answer, ".")
	}
	if record.Host == "" {
		record.Fqdn = mockZoneName + "."
	} else {
		record.Fqdn = record.Host + "." + mockZoneName + "."
	}
	record.DomainName = mockZoneName
	record.ID = m.nextID
	m.nextID++
	return record
}

func (m *mockNameCom) json(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
