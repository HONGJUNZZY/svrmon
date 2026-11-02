package collect

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"svrmon/internal/model"
)

type powerReading struct {
	Watts  float64
	Source string
	Inputs []model.PowerInput
}

var supplyID = regexp.MustCompile(`(?i)(?:crps|psu|ps|power[ _/-]*supply|pwr)[ _/#-]*(\d+)`)
var inputWord = regexp.MustCompile(`(?i)(?:\binput\b|\bpin\b|\bac\b)`)
var instantaneousPower = regexp.MustCompile(`(?im)^\s*Instantaneous power reading\s*:\s*([0-9]+(?:\.[0-9]+)?)\s*(watts|w)\b`)

func sensorWatts(s model.Sensor) (float64, bool) {
	if !s.HasValue || math.IsNaN(s.ValueF) || math.IsInf(s.ValueF, 0) || s.ValueF < 0 {
		return 0, false
	}
	switch strings.ToLower(strings.TrimSpace(s.Unit)) {
	case "w", "watt", "watts":
		return s.ValueF, true
	case "kw", "kilowatt", "kilowatts":
		watts := s.ValueF * 1000
		return watts, !math.IsInf(watts, 0)
	}
	return 0, false
}

// Power selection never treats an arbitrary CPU/GPU watt reading as chassis power.
// Per-PSU inputs are summed once per supply, excluding outputs and nameplates.
func powerFromSensors(sensors []model.Sensor) (powerReading, bool) {
	inputs := map[string]model.PowerInput{}
	expected := map[string]bool{}
	var system []model.PowerInput
	for _, s := range sensors {
		name := strings.ToLower(strings.TrimSpace(s.Name))
		normalized := strings.NewReplacer("_", " ", "-", " ", "/", " ").Replace(name)
		compact := strings.ReplaceAll(normalized, " ", "")
		if strings.Contains(compact, "output") || strings.Contains(compact, "pout") {
			continue
		}
		excluded := false
		for _, term := range []string{"rating", "rated", "capacity", "limit", "maximum", "minimum", "average", "peak"} {
			if strings.Contains(compact, term) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if strings.Contains(" "+normalized+" ", " max ") || strings.Contains(" "+normalized+" ", " min ") {
			continue
		}
		id := supplyID.FindStringSubmatch(name)
		isSupply := len(id) > 1 || strings.Contains(compact, "crps") || strings.Contains(compact, "psu") || strings.Contains(compact, "powersupply")
		isInput := inputWord.MatchString(normalized) || strings.Contains(compact, "input") || strings.HasSuffix(compact, "pin")
		if isSupply && isInput {
			key := compact
			if len(id) > 1 {
				n, _ := strconv.Atoi(id[1])
				key = strconv.Itoa(n)
			}
			expected[key] = true
			if watts, ok := sensorWatts(s); ok {
				// Deterministic alias selection avoids double counting one PSU exposed twice.
				v, exists := inputs[key]
				if !exists || s.Name < v.Name {
					inputs[key] = model.PowerInput{Name: s.Name, Watts: watts}
				}
			}
			continue
		}
		isSystem := strings.Contains(compact, "system") || strings.Contains(compact, "chassis") || strings.Contains(compact, "total") || strings.HasPrefix(compact, "sys") || compact == "pwrconsumption" || compact == "powerconsumption"
		if isSystem && (strings.Contains(compact, "power") || strings.Contains(compact, "pwr") || strings.Contains(compact, "consumption")) {
			if watts, ok := sensorWatts(s); ok {
				system = append(system, model.PowerInput{Name: s.Name, Watts: watts})
			}
		}
	}
	if len(inputs) > 0 && len(inputs) == len(expected) {
		keys := make([]string, 0, len(inputs))
		for k := range inputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		r := powerReading{Source: "ipmi_psu_input"}
		for _, k := range keys {
			r.Inputs = append(r.Inputs, inputs[k])
			r.Watts += inputs[k].Watts
		}
		if math.IsInf(r.Watts, 0) {
			return powerReading{}, false
		}
		return r, true
	}
	// Multiple unrelated totals are ambiguous; a DCMI reading is safer.
	if len(system) == 1 {
		return powerReading{Watts: system[0].Watts, Source: "ipmi_system"}, true
	}
	return powerReading{}, false
}

func parseDCMIPower(output string) (powerReading, bool) {
	if strings.Contains(strings.ToLower(output), "deactivated") || strings.Contains(strings.ToLower(output), "not activated") {
		return powerReading{}, false
	}
	match := instantaneousPower.FindStringSubmatch(output)
	if len(match) < 2 {
		return powerReading{}, false
	}
	watts, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsInf(watts, 0) || math.IsNaN(watts) || watts < 0 {
		return powerReading{}, false
	}
	return powerReading{Watts: watts, Source: "dcmi"}, true
}
