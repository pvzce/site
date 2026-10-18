package workshop

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type ObjectStore interface {
	Put(context.Context, string, io.Reader, int64) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}
type S3 struct {
	client *minio.Client
	bucket string
}

func NewS3(ctx context.Context, s Settings) (*S3, error) {
	if s.Bucket == "" || s.AccessKey == "" || s.SecretKey == "" {
		return nil, fmt.Errorf("S3 credentials and private bucket are required")
	}
	c, e := minio.New(s.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""), Secure: s.Secure})
	if e != nil {
		return nil, fmt.Errorf("invalid S3 configuration")
	}
	obj := &S3{client: c, bucket: s.Bucket}
	if e := obj.private(ctx); e != nil {
		return nil, e
	}
	return obj, nil
}
func (s *S3) private(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	policy, e := s.client.GetBucketPolicy(ctx, s.bucket)
	if e != nil {
		return fmt.Errorf("cannot verify private S3 bucket")
	}
	if strings.TrimSpace(policy) != "" {
		return fmt.Errorf("S3 bucket must have no public policy")
	}
	return s.privateACL(ctx)
}
func (s *S3) Put(ctx context.Context, key string, r io.Reader, n int64) error {
	if e := s.private(ctx); e != nil {
		return ErrUnavailable
	}
	_, e := s.client.PutObject(ctx, s.bucket, key, r, n, minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true})
	return e
}
func (s *S3) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if e := s.private(ctx); e != nil {
		return nil, ErrUnavailable
	}
	o, e := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if e != nil {
		return nil, e
	}
	if _, e = o.Stat(); e != nil {
		_ = o.Close()
		return nil, e
	}
	return o, nil
}
func (s *S3) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

type Scanner interface {
	Scan(context.Context, io.Reader) (string, error)
}

// Clamd streams INSTREAM to a configured, private ClamAV daemon. Any non-OK result fails closed.
type Clamd struct{ Address string }

// A positive control catches degraded ClamAV installations that answer OK after an internal
// temporary-directory failure. A clean upload is accepted only when the same service can
// still detect the standard inert EICAR test signature. Never execute either stream.
const scannerPositiveControl = "X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"

func (c Clamd) Scan(ctx context.Context, r io.Reader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, e := c.scanStream(ctx, r)
	if e != nil || result != "clean" {
		return result, e
	}
	control, e := c.scanStream(ctx, strings.NewReader(scannerPositiveControl))
	if e != nil || control != "infected" {
		return "failed", ErrUnavailable
	}
	return "clean", nil
}
func (c Clamd) scanStream(ctx context.Context, r io.Reader) (string, error) {
	if c.Address == "" {
		return "failed", ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", c.Address)
	if e != nil {
		return "failed", ErrUnavailable
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if e = conn.SetDeadline(deadline); e != nil {
		return "failed", e
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, e = io.WriteString(conn, "zINSTREAM\x00"); e != nil {
		return "failed", e
	}
	b := make([]byte, 32<<10)
	var lenbuf [4]byte
	for {
		n, re := r.Read(b)
		if n > 0 {
			binary.BigEndian.PutUint32(lenbuf[:], uint32(n))
			if _, e = conn.Write(lenbuf[:]); e != nil {
				return "failed", e
			}
			if _, e = io.Copy(conn, bytes.NewReader(b[:n])); e != nil {
				return "failed", e
			}
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			return "failed", re
		}
	}
	binary.BigEndian.PutUint32(lenbuf[:], 0)
	if _, e = conn.Write(lenbuf[:]); e != nil {
		return "failed", e
	}
	response, e := io.ReadAll(io.LimitReader(conn, 4096))
	if e != nil && !errors.Is(e, io.EOF) {
		return "failed", e
	}
	v := strings.TrimRight(string(response), "\x00\r\n")
	if v == "stream: OK" {
		return "clean", nil
	}
	if strings.HasSuffix(v, " FOUND") {
		return "infected", nil
	}
	return "failed", ErrUnavailable
}

// BootstrapStorage creates an initially private bucket using operator credentials.
// Regular server startup only verifies an existing bucket and never changes policies.
func BootstrapStorage(ctx context.Context, s Settings) error {
	if s.Endpoint == "" || s.Bucket == "" || s.AccessKey == "" || s.SecretKey == "" {
		return ErrUnavailable
	}
	c, e := minio.New(s.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(s.AccessKey, s.SecretKey, ""), Secure: s.Secure})
	if e != nil {
		return ErrInvalid
	}
	exists, e := c.BucketExists(ctx, s.Bucket)
	if e != nil {
		return ErrUnavailable
	}
	if !exists {
		if e := c.MakeBucket(ctx, s.Bucket, minio.MakeBucketOptions{}); e != nil {
			return ErrUnavailable
		}
	}
	return (&S3{client: c, bucket: s.Bucket}).private(ctx)
}

// ACL inspection is signed internally; its URL is never returned or logged.
func (s *S3) privateACL(ctx context.Context) error {
	endpoint, e := s.client.Presign(ctx, http.MethodGet, s.bucket, "", time.Minute, url.Values{"acl": {""}})
	if e != nil {
		return fmt.Errorf("cannot inspect S3 bucket ACL")
	}
	request, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if e != nil {
		return ErrUnavailable
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(request)
	if e != nil {
		return fmt.Errorf("cannot inspect S3 bucket ACL")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("cannot verify owner-only S3 bucket ACL")
	}
	var acl struct {
		XMLName xml.Name `xml:"AccessControlPolicy"`
		Owner   struct {
			ID string `xml:"ID"`
		} `xml:"Owner"`
		Grants []struct {
			Grantee struct {
				ID  string `xml:"ID"`
				URI string `xml:"URI"`
			} `xml:"Grantee"`
		} `xml:"AccessControlList>Grant"`
	}
	if xml.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&acl) != nil || acl.Owner.ID == "" || len(acl.Grants) == 0 {
		return fmt.Errorf("invalid S3 bucket ACL")
	}
	for _, grant := range acl.Grants {
		if grant.Grantee.URI != "" || grant.Grantee.ID != acl.Owner.ID {
			return fmt.Errorf("S3 bucket ACL must be owner-only")
		}
	}
	return nil
}
