package namedotcom

import (
	"context"
	"testing"
)

func TestNewNameDotComClient_URLValidation(t *testing.T) {
	tests := []struct {
		server  string
		wantErr bool
	}{
		{"https://api.name.com", false},
		{"https://api.dev.name.com", false},
		{"http://localhost:8080", false},
		{"http://127.0.0.1:9090", false},
		{"https://my-mock.example.org", false},
		{"ftp://api.name.com", true},
		{"api.name.com", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.server, func(t *testing.T) {
			_, err := NewNameDotComClient(context.Background(), "tok", "user", tt.server)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewNameDotComClient(%q) err = %v, wantErr = %v", tt.server, err, tt.wantErr)
			}
		})
	}
}
