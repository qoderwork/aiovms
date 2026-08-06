package schedule

import (
	"testing"
)

func TestContainsWeekday(t *testing.T) {
	tests := []struct {
		name     string
		weekdays string
		day      string
		want     bool
	}{
		{"exact match", "1,2,3,4,5", "1", true},
		{"middle match", "0,1,2,3,4,5,6", "3", true},
		{"last match", "1,2,3,4,5", "5", true},
		{"not present", "1,2,3,4,5", "0", false},
		{"empty weekdays", "", "0", false},
		{"single day match", "3", "3", true},
		{"single day no match", "3", "5", false},
		{"spaces around", " 1 , 2 , 3 ", "2", true},
		{"no space after comma", "1,2,3", "2", true},
		{"weekend only", "0,6", "0", true},
		{"weekend only no match", "0,6", "3", false},
		{"all days", "0,1,2,3,4,5,6", "4", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsWeekday(tt.weekdays, tt.day)
			if got != tt.want {
				t.Errorf("containsWeekday(%q, %q) = %v, want %v", tt.weekdays, tt.day, got, tt.want)
			}
		})
	}
}

func TestValidateTimeRange(t *testing.T) {
	tests := []struct {
		name      string
		start     string
		end       string
		wantError bool
	}{
		{"valid range", "08:00", "20:00", false},
		{"start equals end", "08:00", "08:00", true},
		{"start after end", "20:00", "08:00", true},
		{"empty both", "", "", false},
		{"empty start", "", "20:00", false},
		{"empty end", "08:00", "", false},
		{"midnight range", "00:00", "23:59", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTimeRange(tt.start, tt.end)
			hasErr := err != nil
			if hasErr != tt.wantError {
				t.Errorf("validateTimeRange(%q, %q) error = %v, wantError = %v", tt.start, tt.end, err, tt.wantError)
			}
		})
	}
}
