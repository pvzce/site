package workshop

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Settings struct {
	MaxFile, DailyQuota                                             int64
	Location                                                        *time.Location
	Endpoint, Bucket, AccessKey, SecretKey, HashKey, ScannerAddress string
	Secure                                                          bool
	TrustedProxies                                                  []netip.Prefix
}

func LoadSettings() (Settings, error) {
	s := Settings{MaxFile: 209715200, DailyQuota: 524288000, Location: time.UTC, Secure: true}
	for _, v := range []struct {
		name   string
		target *int64
	}{{"UPLOAD_MAX_FILE_BYTES", &s.MaxFile}, {"UPLOAD_DAILY_QUOTA_BYTES", &s.DailyQuota}} {
		if raw := os.Getenv(v.name); raw != "" {
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || n < 1 || n > 10<<30 {
				return s, fmt.Errorf("%s must be 1..10737418240 bytes", v.name)
			}
			*v.target = n
		}
	}
	if z := os.Getenv("UPLOAD_QUOTA_TIMEZONE"); z != "" {
		l, e := time.LoadLocation(z)
		if e != nil {
			return s, fmt.Errorf("invalid UPLOAD_QUOTA_TIMEZONE")
		}
		s.Location = l
	}
	s.Endpoint = os.Getenv("S3_ENDPOINT")
	s.Bucket = os.Getenv("S3_BUCKET")
	s.AccessKey = os.Getenv("S3_ACCESS_KEY")
	s.SecretKey = os.Getenv("S3_SECRET_KEY")
	s.HashKey = os.Getenv("DOWNLOAD_HASH_KEY")
	s.ScannerAddress = os.Getenv("CLAMD_ADDRESS")
	if raw := os.Getenv("S3_SECURE"); raw != "" {
		b, e := strconv.ParseBool(raw)
		if e != nil {
			return s, fmt.Errorf("invalid S3_SECURE")
		}
		s.Secure = b
	}
	if raw := os.Getenv("TRUSTED_PROXY_CIDRS"); raw != "" {
		for _, v := range strings.Split(raw, ",") {
			p, e := netip.ParsePrefix(strings.TrimSpace(v))
			if e != nil {
				return s, fmt.Errorf("invalid TRUSTED_PROXY_CIDRS")
			}
			s.TrustedProxies = append(s.TrustedProxies, p)
		}
	}
	return s, nil
}
