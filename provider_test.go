package namedotcom

import (
	"context"
	"net/netip"
	"os"
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
			name:    "get_record_1_pass",
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.GetRecords(ctx, zone)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetRecords() error = %v, wantErr %v", err, tt.wantErr)
			} else if len(got) > 0 != tt.want {
				t.Fatalf("GetRecords() error = %v, want %v", err, tt.want)
			} else {
				t.Log(got)
				if recordsAlreadyExistForDomain {
					rollingRecords = got
				}
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
			want:    true,
			wantErr: false,
		},
		{
			name:    "append_record_2_pass",
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.AppendRecords(ctx, zone, newRecordSet)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AppendRecords() error = %v, wantErr %v", err, tt.wantErr)
			} else if len(got) > 0 != tt.want {
				t.Fatalf("AppendRecords() error = %v, want %v", err, tt.want)
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
			name:    "update_record_1_pass",
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log(rollingRecords)

			// With libdns v1.x, SetRecords will automatically look up the record ID
			// by matching name/type/data, so we don't need to manually set it
			got, err := p.SetRecords(ctx, zone, updateSet)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SetRecords() error = %v, wantErr %v", err, tt.wantErr)
			} else if len(got) > 0 && !tt.want {
				t.Fatalf("SetRecords() error = %v, want %v", err, tt.want)
			} else {
				t.Log(got)
				rollingRecords = got
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
			// With libdns v1.x, DeleteRecords will automatically look up the record ID
			// by matching name/type/data, so we don't need to manually set it
			got, err := p.DeleteRecords(ctx, zone, updateSet)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DeleteRecords() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				t.Log(got, err)
			}
		})
	}
}
