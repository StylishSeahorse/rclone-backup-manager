package proto

import "testing"

func TestValidateSchedule(t *testing.T) {
	ok := [][2]string{{"0 2 * * *", "UTC"}, {"*/15 * * * *", "Europe/Berlin"}, {"@daily", "UTC"}, {"@every 6h", "America/New_York"}, {"0 3 * * 0", ""}}
	for _, c := range ok {
		if err := ValidateSchedule(c[0], c[1]); err != nil {
			t.Errorf("%v should be valid: %v", c, err)
		}
	}
	bad := [][2]string{{"", "UTC"}, {"not a cron", "UTC"}, {"61 * * * *", "UTC"}, {"0 2 * * *", "Mars/Olympus"},
		{"CRON_TZ=UTC 0 2 * * *", "UTC"}, {"0 2 * * *\n* * * * *", "UTC"}, {"0 0 0 0 0 0", "UTC"}}
	for _, c := range bad {
		if err := ValidateSchedule(c[0], c[1]); err == nil {
			t.Errorf("%q tz=%q should be invalid", c[0], c[1])
		}
	}
}
