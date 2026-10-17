package workshop

import (
	"testing"
	"time"
)

func TestUploadDefaultsAndOverrides(t *testing.T) {
	for _, key := range []string{"UPLOAD_MAX_FILE_BYTES", "UPLOAD_DAILY_QUOTA_BYTES", "UPLOAD_QUOTA_TIMEZONE", "TRUSTED_PROXY_CIDRS", "S3_SECURE"} {
		t.Setenv(key, "")
	}
	s, e := LoadSettings()
	if e != nil || s.MaxFile != 209715200 || s.DailyQuota != 524288000 || s.Location != time.UTC || !s.Secure {
		t.Fatal(s, e)
	}
	t.Setenv("UPLOAD_MAX_FILE_BYTES", "4096")
	t.Setenv("UPLOAD_DAILY_QUOTA_BYTES", "8192")
	t.Setenv("UPLOAD_QUOTA_TIMEZONE", "Asia/Shanghai")
	s, e = LoadSettings()
	if e != nil || s.MaxFile != 4096 || s.DailyQuota != 8192 || s.Location.String() != "Asia/Shanghai" {
		t.Fatal(s, e)
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "not-a-proxy")
	if _, e = LoadSettings(); e == nil {
		t.Fatal("invalid trusted proxy accepted")
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	t.Setenv("UPLOAD_MAX_FILE_BYTES", "0")
	if _, e = LoadSettings(); e == nil {
		t.Fatal("zero limit accepted")
	}
}

func TestReceiveDeadlineStopsAtConfiguredMidnight(t *testing.T) {
	loc, e := time.LoadLocation("Asia/Shanghai")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Date(2026, 10, 18, 23, 59, 0, 0, loc)
	got := receiveDeadline(now, loc)
	want := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("deadline %v want %v", got, want)
	}
	morning := now.Add(-time.Hour)
	if !receiveDeadline(morning, loc).Equal(morning.Add(10 * time.Minute)) {
		t.Fatal("normal receive deadline changed")
	}
}
