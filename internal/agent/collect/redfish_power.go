package collect

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"svrmon/internal/model"
)

// RedfishPowerConfig reads a standard Chassis Power resource. Appliance TLS
// certificates may be pinned instead of disabling certificate verification.
type RedfishPowerConfig struct {
	URL               string `json:"url"`
	Username          string `json:"username"`
	Password          string `json:"password"`
	CertificateSHA256 string `json:"certificateSHA256,omitempty"`
}

type redfishPower struct {
	cfg    RedfishPowerConfig
	client *http.Client
}

func newRedfishPower(cfg RedfishPowerConfig) (*redfishPower, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("Redfish power URL must use HTTPS without embedded credentials")
	}
	client, err := newBMCHTTPClient(cfg.CertificateSHA256)
	if err != nil {
		return nil, err
	}
	return &redfishPower{cfg: cfg, client: client}, nil
}

func newBMCHTTPClient(certificateSHA256 string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if certificateSHA256 != "" {
		pin, err := hex.DecodeString(strings.ReplaceAll(certificateSHA256, ":", ""))
		if err != nil || len(pin) != sha256.Size {
			return nil, fmt.Errorf("invalid BMC certificate SHA-256 fingerprint")
		}
		transport.TLSClientConfig.InsecureSkipVerify = true // exact leaf pin verified below
		transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("BMC did not present a certificate")
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(pin, sum[:]) != 1 {
				return fmt.Errorf("BMC certificate fingerprint mismatch")
			}
			return nil
		}
	}
	return &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (r *redfishPower) read(ctx context.Context) (powerReading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.URL, nil)
	if err != nil {
		return powerReading{}, fmt.Errorf("invalid Redfish request")
	}
	req.SetBasicAuth(r.cfg.Username, r.cfg.Password)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := r.client.Do(req)
	if err != nil {
		return powerReading{}, fmt.Errorf("Redfish power connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return powerReading{}, fmt.Errorf("Redfish power HTTP %d", resp.StatusCode)
	}
	var payload struct {
		PowerSupplies []struct {
			MemberID        string `json:"MemberId"`
			Name            string
			PowerInputWatts *float64
			Status          struct{ State string }
		}
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return powerReading{}, fmt.Errorf("invalid Redfish power response")
	}
	reading := powerReading{Source: "redfish_psu_input"}
	seen := map[string]bool{}
	for i, s := range payload.PowerSupplies {
		if strings.EqualFold(s.Status.State, "Absent") {
			continue
		}
		if s.PowerInputWatts == nil || math.IsNaN(*s.PowerInputWatts) || math.IsInf(*s.PowerInputWatts, 0) || *s.PowerInputWatts < 0 {
			return powerReading{}, fmt.Errorf("Redfish PSU input reading incomplete")
		}
		id := s.MemberID
		if id == "" {
			id = s.Name
		}
		if id == "" {
			id = fmt.Sprint(i)
		}
		if seen[id] {
			return powerReading{}, fmt.Errorf("duplicate Redfish power supply")
		}
		seen[id] = true
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("PSU %d", i+1)
		}
		reading.Inputs = append(reading.Inputs, model.PowerInput{Name: name, Watts: *s.PowerInputWatts})
		reading.Watts += *s.PowerInputWatts
	}
	if len(reading.Inputs) == 0 {
		return powerReading{}, fmt.Errorf("Redfish PSU input power unavailable")
	}
	if math.IsInf(reading.Watts, 0) {
		return powerReading{}, fmt.Errorf("invalid Redfish PSU input total")
	}
	return reading, nil
}
