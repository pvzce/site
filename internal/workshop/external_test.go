package workshop

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const eicar = "X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"

func TestRealClamAVIntegration(t *testing.T) {
	address := os.Getenv("TEST_CLAMD_ADDRESS")
	if address == "" {
		t.Skip("TEST_CLAMD_ADDRESS required for real ClamAV test")
	}
	c := Clamd{Address: address}
	for _, tc := range []struct{ body, want string }{{"harmless file", "clean"}, {eicar, "infected"}} {
		got, e := c.Scan(context.Background(), strings.NewReader(tc.body))
		if e != nil || got != tc.want {
			t.Fatalf("real ClamAV expected %s, got %s: %v", tc.want, got, e)
		}
	}
}
func TestScannerFailsClosed(t *testing.T) {
	if result, e := (Clamd{}).Scan(context.Background(), strings.NewReader("x")); e == nil || result != "failed" {
		t.Fatal("missing scanner accepted")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = io.ReadAll(conn)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, e := (Clamd{Address: listener.Addr().String()}).Scan(ctx, strings.NewReader("x"))
	if e == nil || result != "failed" {
		t.Fatal("scanner timeout marked clean")
	}
	_ = listener.Close() // Also unblock Accept if the deadline elapsed before dialing.
	<-done
}
func TestRealS3PrivacyIntegration(t *testing.T) {
	if os.Getenv("TEST_S3_ENDPOINT") == "" {
		t.Skip("TEST_S3_ENDPOINT required for actual S3 privacy test")
	}
	s, e := LoadSettings()
	if e != nil {
		t.Fatal(e)
	}
	s.Endpoint = os.Getenv("TEST_S3_ENDPOINT")
	s.Bucket = "site-test-" + strings.ToLower(rand.Text())
	ctx := context.Background()
	if e := BootstrapStorage(ctx, s); e != nil {
		t.Fatal(e)
	}
	obj, e := NewS3(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = obj.client.RemoveBucket(context.Background(), s.Bucket) })
	key := "quarantine/privacy-probe"
	content := []byte("privacy verification fixture")
	if e := obj.Put(ctx, key, bytes.NewReader(content), int64(len(content))); e != nil {
		t.Fatal(e)
	}
	defer obj.Delete(ctx, key)
	r, e := obj.Open(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	actual, e := io.ReadAll(r)
	r.Close()
	if e != nil || !bytes.Equal(actual, content) {
		t.Fatal("S3 roundtrip", e)
	}
	scheme := "https"
	if !s.Secure {
		scheme = "http"
	}
	resp, e := http.Get(scheme + "://" + s.Endpoint + "/" + s.Bucket + "/" + key)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("anonymous object read: HTTP %d (expected 403)", resp.StatusCode)
	}
	// The adapter rejects policy-bearing buckets, including a deliberately configured public test bucket.
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + s.Bucket + `/*"]}]}`
	if e := obj.client.SetBucketPolicy(ctx, s.Bucket, policy); e != nil {
		t.Log("Storage rejects policy mutation; authenticated roundtrip and anonymous denial verified. Adapter policy rejection is covered separately.")
		return
	}
	defer obj.client.SetBucketPolicy(context.Background(), s.Bucket, "")
	if _, e := NewS3(ctx, s); e == nil {
		t.Fatal("public-policy bucket accepted")
	}
	if _, e := obj.Open(ctx, key); e == nil {
		t.Fatal("runtime policy change bypassed privacy gate")
	}
}

func TestS3PublicPolicyRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("location") {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Version":"2012-10-17","Statement":[]}`)
	}))
	defer server.Close()
	s := Settings{Endpoint: strings.TrimPrefix(server.URL, "http://"), Bucket: "private-test", AccessKey: "test-access", SecretKey: "test-secret", Secure: false}
	if _, e := NewS3(context.Background(), s); e == nil {
		t.Fatal("bucket with policy was accepted")
	}
}
