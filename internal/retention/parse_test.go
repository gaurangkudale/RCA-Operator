package retention

import (
	"testing"
	"time"
)

func TestParseIncidentRetention(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		days    int
		want    time.Duration
		wantErr bool
	}{
		{name: "minutes", value: "5m", want: 5 * time.Minute},
		{name: "hours", value: "2h", want: 2 * time.Hour},
		{name: "days", value: "30d", want: 30 * 24 * time.Hour},
		{name: "legacy days fallback", days: 7, want: 7 * 24 * time.Hour},
		{name: "default fallback", want: 30 * 24 * time.Hour},
		{name: "invalid missing suffix", value: "30", wantErr: true},
		{name: "invalid suffix", value: "7w", wantErr: true},
		{name: "invalid zero", value: "0m", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIncidentRetention(tt.value, tt.days)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (duration=%s)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, got)
			}
		})
	}
}
