package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"svrmon/internal/model"
)

// ASUSPowerConfig selects the live ASUS BMC PSU API. Some firmware exposes
// frozen PowerSupplies values in Redfish while this API reads live AC inputs.
type ASUSPowerConfig struct {
	URL               string `json:"url"` // HTTPS origin, without an API path
	Username          string `json:"username"`
	Password          string `json:"password"`
	CertificateSHA256 string `json:"certificateSHA256,omitempty"`
	ExpectedSupplies  int    `json:"expectedSupplies,omitempty"` // slot count, including absent slots
}

type asusPower struct {
	cfg    ASUSPowerConfig
	client *http.Client
	csrf   string
}

func newASUSPower(cfg ASUSPowerConfig) (*asusPower, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || cfg.ExpectedSupplies < 0 {
		return nil, fmt.Errorf("ASUS BMC URL must be an HTTPS origin without credentials, query or API path")
	}
	client, err := newBMCHTTPClient(cfg.CertificateSHA256)
	if err != nil {
		return nil, err
	}
	client.Jar, _ = cookiejar.New(nil)
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &asusPower{cfg: cfg, client: client}, nil
}

func (a *asusPower) login(ctx context.Context) error {
	form := url.Values{"username": {a.cfg.Username}, "password": {a.cfg.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.URL+"/api/session", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("invalid ASUS BMC login request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("ASUS BMC login connection failed")
	}
	defer resp.Body.Close()
	var session struct {
		OK   *int   `json:"ok"`
		CSRF string `json:"CSRFToken"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&session) != nil || session.OK == nil || *session.OK != 0 || session.CSRF == "" {
		return fmt.Errorf("ASUS BMC authentication failed")
	}
	a.csrf = session.CSRF
	return nil
}

func (a *asusPower) read(ctx context.Context) (powerReading, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	// Reuse one session; renew it only when the BMC reports expiration.
	for attempt := 0; attempt < 2; attempt++ {
		if a.csrf == "" {
			if err := a.login(ctx); err != nil {
				return powerReading{}, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.URL+"/api/host_inventory/host_interface_power_supp", nil)
		if err != nil {
			return powerReading{}, fmt.Errorf("invalid ASUS PSU input request")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("X-CSRFTOKEN", a.csrf)
		resp, err := a.client.Do(req)
		if err != nil {
			return powerReading{}, fmt.Errorf("ASUS PSU input connection failed")
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			a.csrf = ""
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return powerReading{}, fmt.Errorf("ASUS PSU input HTTP %d", resp.StatusCode)
		}
		reading, err := parseASUSPower(io.LimitReader(resp.Body, 2<<20), a.cfg.ExpectedSupplies)
		resp.Body.Close()
		return reading, err
	}
	return powerReading{}, fmt.Errorf("ASUS BMC session expired")
}

func asusWatts(raw json.RawMessage) (float64, bool) {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, `"`) {
		if json.Unmarshal(raw, &s) != nil {
			return 0, false
		}
	}
	w, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return w, err == nil && !math.IsNaN(w) && !math.IsInf(w, 0) && w >= 0
}

func parseASUSPower(body io.Reader, expected int) (powerReading, error) {
	var payload []struct {
		Supplies []struct {
			ID     *int            `json:"id"`
			Name   string          `json:"NAME"`
			State  string          `json:"EXIST"`
			Input  json.RawMessage `json:"ACPIN"`
			Output json.RawMessage `json:"DCPOUT"`
		} `json:"Powerinfo"`
	}
	if json.NewDecoder(body).Decode(&payload) != nil {
		return powerReading{}, fmt.Errorf("invalid ASUS PSU input response")
	}
	r := powerReading{Source: "asus_psu_input"}
	seen := map[int]bool{}
	for _, group := range payload {
		for _, s := range group.Supplies {
			if s.ID == nil || seen[*s.ID] {
				return powerReading{}, fmt.Errorf("missing or duplicate ASUS PSU identity")
			}
			seen[*s.ID] = true
			if strings.EqualFold(s.State, "Absent") || strings.EqualFold(s.State, "Not Present") {
				continue
			}
			if !strings.EqualFold(s.State, "Present") {
				return powerReading{}, fmt.Errorf("ASUS PSU presence unavailable")
			}
			w, ok := asusWatts(s.Input)
			if !ok {
				return powerReading{}, fmt.Errorf("ASUS PSU AC input reading incomplete")
			}
			// This firmware sometimes returns transient ACPIN=0 with a live
			// nonzero DC output. Do not report that as a partial chassis total.
			if out, valid := asusWatts(s.Output); valid && out > w+math.Max(5, w*0.05) {
				return powerReading{}, fmt.Errorf("ASUS PSU AC input inconsistent with DC output")
			}
			name := s.Name
			if name == "" {
				name = fmt.Sprintf("PSU %d", *s.ID+1)
			}
			r.Inputs = append(r.Inputs, model.PowerInput{Name: name, Watts: w})
			r.Watts += w
		}
	}
	if len(r.Inputs) == 0 || (expected > 0 && len(seen) != expected) || math.IsInf(r.Watts, 0) {
		return powerReading{}, fmt.Errorf("ASUS PSU AC input reading incomplete")
	}
	return r, nil
}
