package namedotcom

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

var (
	p                            *Provider
	ctx                          = context.Background()
	zone                         string
	recordsAlreadyExistForDomain = true
	rollingRecords               []libdns.Record
	newRecordSet                 []libdns.Record
	updateSet                    []libdns.Record
)

func init() {
	zone = os.Getenv("namedotcom_test_zone")
	p = &Provider{
		Token:  os.Getenv("namedotcom_api_key"),
		User:   os.Getenv("namedotcom_user_name"),
		Server: os.Getenv("namedotcom_server"),
	}

	newRecordSet = []libdns.Record{
		libdns.TXT{
			Name: "__test_txt_record.example.com",
			TTL:  time.Duration(300),
			Text: "old_value",
		},
		libdns.Address{
			Name: "test2",
			TTL:  time.Duration(300),
			IP:   netip.MustParseAddr("10.10.0.2"),
		},
	}

	updateSet = []libdns.Record{
		libdns.TXT{
			Name: "__test_txt_record.example.com",
			TTL:  time.Duration(300),
			Text: "new_value",
		},
		libdns.Address{
			Name: "test2",
			TTL:  time.Duration(300),
			IP:   netip.MustParseAddr("10.10.0.2"),
		},
	}
}

func TestProvider_GetRecords(t *testing.T) {
	tests := []struct {
		name    string
		want    bool
		wantErr bool
	}{
		{
			name:    "get_records_1_pass",
			want:    recordsAlreadyExistForDomain,
			wantErr: !recordsAlreadyExistForDomain,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.GetRecords(ctx, zone)

			if (err != nil) != tt.wantErr {
				t.Fatalf("GetRecords() error = %v, wantErr %v", err, tt.wantErr)
			} else if len(got) == 0 && tt.want == true {
				t.Fatalf("GetRecords() error = %v, want %v", err, tt.want)
			} else {
				t.Log(got, err)
				rollingRecords = got
			}
		})
	}
}

func TestProvider_AppendRecords(t *testing.T) {
	tests := []struct {
		name    string
		want    bool
		wantErr bool
	}{
		{
			name:    "append_record_1_pass",
			want:    len(rollingRecords) > 0,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.AppendRecords(ctx, zone, newRecordSet)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AppendRecords() error = %v, wantErr %v", err, tt.wantErr)
			} else if len(got) < 1 && tt.want {
				t.Fatalf("AppendRecords() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				t.Log(got)
				rollingRecords = got
			}
		})
	}
}

func TestProvider_SetRecords(t *testing.T) {
	tests := []struct {
		name    string
		want    bool
		wantErr bool
	}{
		{
			name:    "set_record_1_pass",
			want:    recordsAlreadyExistForDomain,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testName := strings.ToLower(updateSet[0].RR().Name)
			t.Log(rollingRecords)

			for _, rec := range rollingRecords {
				if testName == rec.RR().Name {
					updateSet[0].ID = rec.ID
				}
			}

			if updateSet[0].ID != "" {
				got, err := p.SetRecords(ctx, zone, updateSet)
				if (err != nil) != tt.wantErr {
					t.Fatalf("SetRecords() error = %v, wantErr %v", err, tt.wantErr)
				} else if len(got) > 0 && !tt.want {
					t.Fatalf("SetRecords() error = %v, want %v", err, tt.want)
				} else {
					t.Log(got)
					rollingRecords = got
				}
			} else {
				t.Log("skipping, record id is not set.")
				t.Skip()
			}
		})
	}
}

func TestProvider_DeleteRecords(t *testing.T) {
	tests := []struct {
		name    string
		want    bool
		wantErr bool
	}{
		{
			name:    "delete_record_1_pass",
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testName := strings.ToLower(updateSet[0].RR().Name)

			for _, rec := range rollingRecords {
				if testName == rec.RR().Name {
					updateSet[0].ID = rec.ID
				}
			}

			if updateSet[0].ID != "" {
				got, err := p.DeleteRecords(ctx, zone, updateSet)
				if (err != nil) != tt.wantErr {
					t.Fatalf("DeleteRecords() error = %v, wantErr %v", err, tt.wantErr)
				} else {
					t.Log(got, err)
				}
			} else {
				t.Log("skipping, record id is not set.")
				t.Skip()
			}
		})
	}
}
