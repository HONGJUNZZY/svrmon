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

func TestASUSInputValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		want  float64
		valid bool
	}{
		{"inputs not outputs or capacity", `[{"Powerinfo":[{"id":0,"EXIST":"Present","ACPIN":"171","DCPOUT":"162","POUTMAX":"800"},{"id":1,"EXIST":"Present","ACPIN":"174","DCPOUT":"164"}]}]`, 345, true},
		{"measured zero", `[{"Powerinfo":[{"id":0,"EXIST":"Present","ACPIN":"0","DCPOUT":"0"},{"id":1,"EXIST":"Present","ACPIN":0,"DCPOUT":0}]}]`, 0, true},
		{"absent slot", `[{"Powerinfo":[{"id":0,"EXIST":"Present","ACPIN":171},{"id":1,"EXIST":"Absent"}]}]`, 171, true},
		{"missing slot", `[{"Powerinfo":[{"id":0,"EXIST":"Present","ACPIN":171}]}]`, 0, false},
		{"transient zero", `[{"Powerinfo":[{"id":0,"EXIST":"Present","ACPIN":"0","DCPOUT":"152"},{"id":1,"EXIST":"Present","ACPIN":"169"}]}]`, 0, false},
		{"unknown input", `[{"Powerinfo":[{"id":0,"EXIST":"Present","ACPIN":"NA"},{"id":1,"EXIST":"Present","ACPIN":169}]}]`, 0, false},
		{"output only", `[{"Powerinfo":[{"id":0,"EXIST":"Present","DCPOUT":152},{"id":1,"EXIST":"Present","DCPOUT":155}]}]`, 0, false},
		{"duplicate slot", `[{"Powerinfo":[{"id":0,"EXIST":"Present","ACPIN":171},{"id":0,"EXIST":"Present","ACPIN":174}]}]`, 0, false},
		{"missing identity", `[{"Powerinfo":[{"EXIST":"Present","ACPIN":171},{"id":1,"EXIST":"Present","ACPIN":174}]}]`, 0, false},
		{"unknown presence", `[{"Powerinfo":[{"id":0,"ACPIN":171},{"id":1,"EXIST":"Present","ACPIN":174}]}]`, 0, false},
		{"empty", `[]`, 0, false},
		{"HTML login", `<html>Login</html>`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := parseASUSPower(strings.NewReader(tc.body), 2)
			if tc.valid && (err != nil || r.Watts != tc.want || r.Source != "asus_psu_input") {
				t.Fatalf("reading %+v, error %v", r, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("accepted unreliable inputs: %+v", r)
			}
		})
	}
	for _, raw := range []string{`null`, `""`, `"NaN"`, `"Inf"`, `"-1"`, `true`, `"1e999"`} {
		if _, ok := asusWatts([]byte(raw)); ok {
			t.Fatalf("accepted invalid input %s", raw)
		}
	}
}

func TestASUSLiveSamplingAndSessionRenewal(t *testing.T) {
	logins, samples, gets := 0, 0, 0
	fail := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/session":
			if req.Method != "POST" {
				t.Error("unexpected BMC mutation")
			}
			_ = req.ParseForm()
			if req.Form.Get("username") != "monitor" || req.Form.Get("password") != "private-test-password" {
				t.Error("incorrect authentication")
			}
			logins++
			http.SetCookie(w, &http.Cookie{Name: "session", Value: fmt.Sprint(logins), Secure: true, Path: "/"})
			fmt.Fprint(w, `{"ok":0,"CSRFToken":"test-csrf"}`)
		case "/api/host_inventory/host_interface_power_supp":
			gets++
			cookie, err := req.Cookie("session")
			if req.Method != "GET" || err != nil || cookie.Value != fmt.Sprint(logins) || req.Header.Get("X-CSRFTOKEN") != "test-csrf" || req.Header.Get("Cache-Control") != "no-cache" {
				t.Error("incorrect authenticated telemetry request")
			}
			if fail {
				http.Error(w, "BMC unavailable", 500)
				return
			}
			if gets == 3 { // expiration after two successful readings
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			samples++
			fmt.Fprintf(w, `[{"Powerinfo":[{"id":0,"NAME":"PSU 1","EXIST":"Present","ACPIN":"%d","DCPOUT":"140"},{"id":1,"NAME":"PSU 2","EXIST":"Present","ACPIN":"169","DCPOUT":"155"}]}]`, 165+samples)
		default:
			t.Errorf("unexpected endpoint %s", req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := ASUSPowerConfig{URL: server.URL, Username: "monitor", Password: "private-test-password", CertificateSHA256: fmt.Sprintf("%x", sha256.Sum256(server.Certificate().Raw)), ExpectedSupplies: 2}
	a, err := newASUSPower(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{335, 336, 337} {
		r, err := a.read(context.Background())
		if err != nil || r.Watts != want || len(r.Inputs) != 2 {
			t.Fatalf("sample %d: %+v %v", i, r, err)
		}
		if i == 1 && logins != 1 {
			t.Fatal("session was not reused")
		}
	}
	if logins != 2 {
		t.Fatalf("session renewal failed: %d logins", logins)
	}
	// A failed live API must neither repeat its last reading nor silently
	// fall back to the known frozen Redfish PSU fields.
	_, frozen := redfishFixture(t, `{"PowerSupplies":[{"MemberId":"0","PowerInputWatts":166},{"MemberId":"1","PowerInputWatts":168}]}`)
	c := NewIPMICollector("missing-ipmitool", 30, 60)
	c.asus, c.redfish = a, frozen
	c.lastErr = "ipmitool not found"
	c.failUntil = time.Now().Add(time.Hour)
	snap := &model.Snapshot{}
	c.Fill(context.Background(), snap)
	if !snap.IPMI.PowerAvailable || snap.IPMI.PowerWatts != 338 || snap.IPMI.PowerSource != "asus_psu_input" {
		t.Fatalf("collector did not select live inputs: %+v", snap.IPMI)
	}
	fail = true
	c.powerAt = time.Time{}
	c.Fill(context.Background(), snap)
	if snap.IPMI.PowerAvailable || snap.IPMI.PowerWatts != 0 || len(snap.IPMI.PowerInputs) != 0 || snap.IPMI.PowerError == "" {
		t.Fatalf("failed request retained stale telemetry: %+v", snap.IPMI)
	}
	// TLS verification must also protect the web login credentials.
	cfg.CertificateSHA256 = strings.Repeat("00", 32)
	bad, _ := newASUSPower(cfg)
	if _, err := bad.read(context.Background()); err == nil || logins != 2 {
		t.Fatal("incorrect certificate pin was accepted")
	}
	for _, url := range []string{"http://bmc.invalid", "https://user:password@bmc.invalid", "https://bmc.invalid/api", "https://bmc.invalid?query=1"} {
		if _, err := newASUSPower(ASUSPowerConfig{URL: url}); err == nil {
			t.Fatalf("accepted unsafe BMC URL %q", url)
		}
	}
}
