package tools

import (
	"strconv"
	"strings"
	"testing"
)

// Real sentences captured from an attached u-blox 7 receiver on this
// host — used verbatim (including checksums) so the parser is tested
// against actual hardware output, not hand-crafted fixtures.
const sampleNMEACycle = "$GPTXT,01,01,02,u-blox ag - www.u-blox.com*50\r\n" +
	"$GPRMC,031517.00,A,4052.13148,N,11152.10040,W,0.022,,190826,,,D*6D\r\n" +
	"$GPVTG,,T,,M,0.022,N,0.040,K,D*22\r\n" +
	"$GPGGA,031517.00,4052.13148,N,11152.10040,W,2,12,0.71,1449.7,M,-18.7,M,,0000*5A\r\n" +
	"$GPGSA,A,3,09,08,27,46,48,07,14,22,05,21,30,17,1.26,0.71,1.04*0F\r\n" +
	"$GPGLL,4052.13148,N,11152.10040,W,031517.00,A,D*70\r\n"

func TestVerifyNMEAChecksum(t *testing.T) {
	if !verifyNMEAChecksum("$GPRMC,031517.00,A,4052.13148,N,11152.10040,W,0.022,,190826,,,D*6D") {
		t.Error("valid real-world checksum rejected")
	}
	if verifyNMEAChecksum("$GPRMC,031517.00,A,4052.13148,N,11152.10040,W,0.022,,190826,,,D*00") {
		t.Error("corrupted checksum accepted")
	}
	if verifyNMEAChecksum("no dollar sign*00") {
		t.Error("sentence without $ prefix accepted")
	}
}

func TestParseNMEACoord(t *testing.T) {
	lat, err := parseNMEACoord("4052.13148", "N")
	if err != nil {
		t.Fatalf("parse latitude: %v", err)
	}
	if diff := lat - 40.868858; diff < -1e-5 || diff > 1e-5 {
		t.Errorf("latitude = %v, want ~40.868858", lat)
	}

	lon, err := parseNMEACoord("11152.10040", "W")
	if err != nil {
		t.Fatalf("parse longitude: %v", err)
	}
	if diff := lon - (-111.868340); diff < -1e-4 || diff > 1e-4 {
		t.Errorf("longitude = %v, want ~-111.868340", lon)
	}
}

func TestParseGPRMC(t *testing.T) {
	fields := strings.Split("GPRMC,031517.00,A,4052.13148,N,11152.10040,W,0.022,,190826,,,D", ",")
	lat, lon, speedKMH, valid, err := parseGPRMC(fields)
	if err != nil {
		t.Fatalf("parseGPRMC: %v", err)
	}
	if !valid {
		t.Fatal("expected a valid (status A) fix")
	}
	if lat < 40.86 || lat > 40.87 || lon < -111.87 || lon > -111.86 {
		t.Errorf("position = (%v, %v), want roughly (40.87, -111.87)", lat, lon)
	}
	if speedKMH < 0 || speedKMH > 1 {
		t.Errorf("speedKMH = %v, want a near-zero value for 0.022 knots", speedKMH)
	}

	voidFields := strings.Split("GPRMC,031517.00,V,,,,,,,190826,,,N", ",")
	_, _, _, valid, err = parseGPRMC(voidFields)
	if err != nil {
		t.Fatalf("void RMC should not be a parse error: %v", err)
	}
	if valid {
		t.Error("status V (void) sentence reported as valid")
	}
}

func TestParseGPGGA(t *testing.T) {
	fields := strings.Split("GPGGA,031517.00,4052.13148,N,11152.10040,W,2,12,0.71,1449.7,M,-18.7,M,,0000", ",")
	lat, lon, altitudeM, quality, err := parseGPGGA(fields)
	if err != nil {
		t.Fatalf("parseGPGGA: %v", err)
	}
	if quality != 2 {
		t.Errorf("quality = %d, want 2 (DGPS)", quality)
	}
	if altitudeM != 1449.7 {
		t.Errorf("altitudeM = %v, want 1449.7", altitudeM)
	}
	if lat < 40.86 || lat > 40.87 || lon < -111.87 || lon > -111.86 {
		t.Errorf("position = (%v, %v), want roughly (40.87, -111.87)", lat, lon)
	}

	noFix := strings.Split("GPGGA,031517.00,,,,,0,00,99.99,,,,,,,", ",")
	_, _, _, quality, err = parseGPGGA(noFix)
	if err != nil {
		t.Fatalf("no-fix GGA should not be a parse error: %v", err)
	}
	if quality != 0 {
		t.Errorf("quality = %d, want 0 (no fix)", quality)
	}
}

func TestReadNMEAFix_RealCapturedCycle(t *testing.T) {
	result, err := readNMEAFix(strings.NewReader(sampleNMEACycle), gpsMaxNMEALines)
	if err != nil {
		t.Fatalf("readNMEAFix: %v", err)
	}
	if result["source"] != "serial" {
		t.Errorf("source = %v, want serial", result["source"])
	}
	if result["altitude_m"] != 1449.7 {
		t.Errorf("altitude_m = %v, want 1449.7", result["altitude_m"])
	}
	lat, _ := result["latitude"].(float64)
	if lat < 40.86 || lat > 40.87 {
		t.Errorf("latitude = %v, want roughly 40.87", lat)
	}
	// This capture is from a stationary receiver (0.022 knots ≈ 0.04
	// km/h, pure GPS noise) — floored to a clean 0, not reported as real
	// motion. See gpsStationarySpeedThresholdKMH.
	if result["speed_kmh"] != 0.0 {
		t.Errorf("speed_kmh = %v, want 0 (stationary-noise floor)", result["speed_kmh"])
	}
}

func TestReadNMEAFix_NoFixYet(t *testing.T) {
	noFixCycle := "$GPRMC,031517.00,V,,,,,,,190826,,,N*78\r\n" +
		"$GPGGA,031517.00,,,,,0,00,99.99,,,,,,,*4B\r\n"
	if _, err := readNMEAFix(strings.NewReader(noFixCycle), gpsMaxNMEALines); err == nil {
		t.Error("expected an error when no sentence has an active fix")
	}
}

func TestReadNMEAFix_SkipsCorruptedLines(t *testing.T) {
	withGarbage := "garbage line with no checksum at all\r\n" +
		"$GPRMC,031517.00,A,4052.13148,N,11152.10040,W,0.022,,190826,,,D*00\r\n" + // bad checksum
		sampleNMEACycle
	result, err := readNMEAFix(strings.NewReader(withGarbage), gpsMaxNMEALines)
	if err != nil {
		t.Fatalf("readNMEAFix should skip bad lines and still find the good cycle: %v", err)
	}
	if result["altitude_m"] != 1449.7 {
		t.Errorf("altitude_m = %v, want 1449.7", result["altitude_m"])
	}
}

// nmeaChecksum computes the XOR checksum verifyNMEAChecksum expects, for
// building synthetic sentences in tests.
func nmeaChecksum(sentence string) string {
	star := strings.IndexByte(sentence, '*')
	if star < 0 {
		star = len(sentence)
	}
	var sum byte
	for i := 1; i < star; i++ {
		sum ^= sentence[i]
	}
	return strings.ToUpper(strconv.FormatUint(uint64(sum), 16))
}

func TestReadNMEAFix_RealMotionSurvivesTheStationaryFloor(t *testing.T) {
	body := "$GPRMC,031517.00,A,4052.13148,N,11152.10040,W,10.0,,190826,,,D"
	rmc := body + "*" + nmeaChecksum(body) + "\r\n"
	// The RMC line here is the moving one (10 knots), not
	// sampleNMEACycle's own stationary one — reusing just its GGA line
	// (for altitude, to make readNMEAFix stop scanning) rather than the
	// whole cycle, so the last-seen speed before that stop is the moving
	// reading, not overwritten by a second, stationary RMC.
	gga := "$GPGGA,031517.00,4052.13148,N,11152.10040,W,2,12,0.71,1449.7,M,-18.7,M,,0000*5A\r\n"
	result, err := readNMEAFix(strings.NewReader(rmc+gga), gpsMaxNMEALines)
	if err != nil {
		t.Fatalf("readNMEAFix: %v", err)
	}
	// 10 knots ~= 18.52 km/h — real motion, must not be floored to 0 the
	// way the stationary 0.022-knot noise in sampleNMEACycle is.
	speed, _ := result["speed_kmh"].(float64)
	if speed < 18 || speed > 19 {
		t.Errorf("speed_kmh = %v, want ~18.52 (10 knots, real motion, not floored)", speed)
	}
}
