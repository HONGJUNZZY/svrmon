package collect

import (
	"math"
	"svrmon/internal/model"
	"testing"
)

func watt(name string, value float64) model.Sensor {
	return model.Sensor{Name: name, ValueF: value, HasValue: true, Unit: "Watts", Status: "ok"}
}

func TestPSUInputSum(t *testing.T) {
	s := []model.Sensor{watt("PSU1 Input Power", 166), watt("PSU2 Input Power", 168), watt("PSU1 Output Power", 153), watt("PSU2 Output Power", 154), watt("PSU1 Rated Capacity", 800), watt("CPU Package Power", 250), watt("System Power", 336)}
	r, ok := powerFromSensors(s)
	if !ok || r.Watts != 334 || r.Source != "ipmi_psu_input" || len(r.Inputs) != 2 {
		t.Fatalf("input sum: %+v %v", r, ok)
	}
	s = append(s, watt("PS1 Input Power", 166))
	r, ok = powerFromSensors(s)
	if !ok || r.Watts != 334 || len(r.Inputs) != 2 {
		t.Fatal("PSU alias was counted twice")
	}
}

func TestPowerDoesNotInventTotals(t *testing.T) {
	for _, s := range [][]model.Sensor{
		{watt("CPU Power", 250), watt("GPU Power", 300)},
		{watt("Board Power", 110)},
		{watt("PSU1 Output Power", 153), watt("PSU2 Output Power", 154)},
		{watt("PSU1 Input Power", 166), {Name: "PSU2 Input Power", Value: "na", Unit: "Watts"}},
		{watt("PSU1 Input Power", math.NaN())},
		{watt("PSU1 Input Power", math.Inf(1))},
		{watt("PSU1 Input Power", -10)},
	} {
		if r, ok := powerFromSensors(s); ok {
			t.Fatalf("unreliable total accepted: %+v", r)
		}
	}
	r, ok := powerFromSensors([]model.Sensor{watt("PSU1 Input Power", 0), watt("PSU2 Input Power", 0)})
	if !ok || r.Watts != 0 {
		t.Fatal("zero is a valid measured input")
	}
	r, ok = powerFromSensors([]model.Sensor{{Name: "System Power", Unit: "kW", HasValue: true, ValueF: 0.334}})
	if !ok || r.Watts != 334 || r.Source != "ipmi_system" {
		t.Fatal("system kW reading failed")
	}
}

func TestDCMIUsesInstantaneousReading(t *testing.T) {
	text := "Instantaneous power reading: 334 Watts\nMinimum during sampling period: 308 Watts\nMaximum during sampling period: 669 Watts\nAverage power reading over sample period: 339 Watts\nPower reading state is: activated\n"
	r, ok := parseDCMIPower(text)
	if !ok || r.Watts != 334 || r.Source != "dcmi" {
		t.Fatalf("wrong DCMI reading: %+v", r)
	}
	if _, ok := parseDCMIPower("Maximum power: 669 Watts"); ok {
		t.Fatal("accepted a maximum instead of live reading")
	}
	if _, ok := parseDCMIPower(text + "Power reading state is: deactivated"); ok {
		t.Fatal("accepted deactivated measurement")
	}
}
