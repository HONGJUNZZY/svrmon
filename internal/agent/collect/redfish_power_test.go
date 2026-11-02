package collect

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"svrmon/internal/model"
)

func redfishFixture(t *testing.T, body string) (*httptest.Server, *redfishPower) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "test" || p != "test-password" || r.Method != "GET" {
			t.Error("wrong Redfish request")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	pin := fmt.Sprintf("%x", sha256.Sum256(server.Certificate().Raw))
	reader, err := newRedfishPower(RedfishPowerConfig{URL: server.URL, Username: "test", Password: "test-password", CertificateSHA256: pin})
	if err != nil {
		t.Fatal(err)
	}
	return server, reader
}

func TestRedfishAddsACInputsNotOutputs(t *testing.T) {
	_, reader := redfishFixture(t, `{"PowerSupplies":[{"MemberId":"0","Name":"Power Supply 1","PowerInputWatts":166,"PowerOutputWatts":153,"PowerCapacityWatts":800},{"MemberId":"1","Name":"Power Supply 2","PowerInputWatts":168,"PowerOutputWatts":154,"PowerCapacityWatts":800}],"PowerControl":[{"PowerConsumedWatts":336}]}`)
	r, err := reader.read(context.Background())
	if err != nil || r.Watts != 334 || r.Source != "redfish_psu_input" || len(r.Inputs) != 2 {
		t.Fatalf("wrong input sum: %+v %v", r, err)
	}
	c := NewIPMICollector("missing", 30, 60)
	c.redfish = reader
	c.lastErr = "ipmitool not found"
	c.failUntil = time.Now().Add(time.Hour)
	snap := &model.Snapshot{}
	c.Fill(context.Background(), snap)
	if !snap.IPMI.PowerAvailable || snap.IPMI.PowerWatts != 334 || !snap.IPMI.Available {
		t.Fatal("Redfish power should work independently of local IPMI")
	}
}

func TestRedfishRejectsPartialAndOutputOnlyReadings(t *testing.T) {
	for _, body := range []string{
		`{"PowerSupplies":[{"Name":"PSU1","PowerInputWatts":166},{"Name":"PSU2","PowerOutputWatts":154}]}`,
		`{"PowerSupplies":[{"Name":"PSU1","PowerOutputWatts":153}]}`,
		`{"PowerSupplies":[{"MemberId":"0","PowerInputWatts":166},{"MemberId":"0","PowerInputWatts":166}]}`,
		`{"PowerSupplies":[]}`,
	} {
		_, reader := redfishFixture(t, body)
		if _, err := reader.read(context.Background()); err == nil {
			t.Fatalf("accepted incomplete inputs: %s", body)
		}
	}
	_, reader := redfishFixture(t, `{"PowerSupplies":[{"Name":"PSU1","PowerInputWatts":0},{"Name":"PSU2","PowerInputWatts":0}]}`)
	if r, err := reader.read(context.Background()); err != nil || r.Watts != 0 || len(r.Inputs) != 2 {
		t.Fatal("zero inputs must remain valid")
	}
}

func TestRedfishPinsCertificateAndProtectsCredentials(t *testing.T) {
	server, reader := redfishFixture(t, `{"PowerSupplies":[{"Name":"PSU1","PowerInputWatts":166}]}`)
	cfg := reader.cfg
	cfg.CertificateSHA256 = strings.Repeat("00", 32)
	bad, err := newRedfishPower(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bad.read(context.Background()); err == nil {
		t.Fatal("wrong BMC certificate was trusted")
	}
	cfg.CertificateSHA256 = ""
	strict, _ := newRedfishPower(cfg)
	if _, err = strict.read(context.Background()); err == nil {
		t.Fatal("untrusted self-signed certificate was accepted without a pin")
	}
	for _, url := range []string{"http://example.invalid/power", "https://user:password@example.invalid/power"} {
		if _, err := newRedfishPower(RedfishPowerConfig{URL: url}); err == nil {
			t.Fatal("insecure credential transport was allowed")
		}
	}
	_ = server
}
