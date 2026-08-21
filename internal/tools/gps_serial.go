package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.bug.st/serial"
)

// gpsMaxNMEALines bounds how many sentences readNMEAFix scans looking for
// both a position and an altitude before giving up — real hardware streams
// a full cycle (RMC/VTG/GGA/GSA/GSV.../GLL) roughly once a second, so this
// comfortably covers a few seconds even with a weak antenna, without
// risking an unbounded scan if the receiver never gets a fix.
const gpsMaxNMEALines = 120

// gpsStationarySpeedThresholdKMH: reported speeds below this are floored to
// zero — see readNMEAFix.
const gpsStationarySpeedThresholdKMH = 1.0

// readSerialGPS opens the configured serial port and reads live NMEA 0183
// sentences from a real GPS receiver. Closing the port when ctx is done
// unblocks a pending read, so this is cancellable the same way any other
// tool call is (e.g. by the overall chat request timeout).
func (t *GPSTool) readSerialGPS(ctx context.Context) (any, error) {
	if t.config.SerialPort == "" {
		return nil, fmt.Errorf("gps serial_port is not configured")
	}
	baud := t.config.BaudRate
	if baud <= 0 {
		baud = 9600
	}

	port, err := serial.Open(t.config.SerialPort, &serial.Mode{BaudRate: baud})
	if err != nil {
		return nil, fmt.Errorf("open GPS serial port %q: %w", t.config.SerialPort, err)
	}
	defer port.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			port.Close() // unblocks a pending Read with an error
		case <-done:
		}
	}()

	return readNMEAFix(port, gpsMaxNMEALines)
}

// readNMEAFix scans NMEA 0183 sentences from r until it has both a
// position (from an RMC sentence with an active fix) and an altitude
// (from a GGA sentence with a non-zero fix quality), or maxLines is
// reached first. Malformed, unrelated, or checksum-failed lines are
// skipped, not fatal — real serial data is noisy (a partial line right
// after opening, GSA/GSV satellite chatter, occasional corruption).
func readNMEAFix(r io.Reader, maxLines int) (map[string]any, error) {
	scanner := bufio.NewScanner(r)
	var lat, lon, speedKMH, altitudeM float64
	havePosition, haveAltitude := false, false

	for i := 0; i < maxLines && scanner.Scan(); i++ {
		line := strings.TrimSpace(scanner.Text())
		if !verifyNMEAChecksum(line) {
			continue
		}
		star := strings.IndexByte(line, '*')
		fields := strings.Split(strings.TrimPrefix(line[:star], "$"), ",")
		if len(fields) == 0 {
			continue
		}

		switch {
		case strings.HasSuffix(fields[0], "RMC"):
			if plat, plon, pspeed, valid, err := parseGPRMC(fields); err == nil && valid {
				lat, lon, speedKMH = plat, plon, pspeed
				havePosition = true
			}
		case strings.HasSuffix(fields[0], "GGA"):
			if plat, plon, palt, quality, err := parseGPGGA(fields); err == nil && quality > 0 {
				lat, lon, altitudeM = plat, plon, palt
				havePosition = true
				haveAltitude = true
			}
		}

		if havePosition && haveAltitude {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read NMEA stream: %w", err)
	}
	if !havePosition {
		return nil, fmt.Errorf("no GPS fix yet — check antenna placement and try again")
	}

	// A stationary receiver still reports tiny nonzero speeds (GPS
	// position/velocity noise, not real motion) — e.g. 0.022 knots ≈ 0.04
	// km/h — which reads as nonsense both in chat ("скорость 0.12 км/ч"
	// while parked) and as jitter on the monitoring dashboard
	// (docs/monitoring.md). Below this threshold, report a clean 0.
	if speedKMH < gpsStationarySpeedThresholdKMH {
		speedKMH = 0
	}

	result := map[string]any{
		"latitude":  lat,
		"longitude": lon,
		"speed_kmh": speedKMH,
		"source":    "serial",
	}
	if haveAltitude {
		result["altitude_m"] = altitudeM
	}
	return result, nil
}

// verifyNMEAChecksum reports whether sentence (e.g. "$GPRMC,...*6D") has a
// valid trailing checksum: the XOR of every byte between "$" and "*".
func verifyNMEAChecksum(sentence string) bool {
	if !strings.HasPrefix(sentence, "$") {
		return false
	}
	star := strings.IndexByte(sentence, '*')
	if star < 1 || star+3 > len(sentence) {
		return false
	}
	want, err := strconv.ParseUint(sentence[star+1:star+3], 16, 8)
	if err != nil {
		return false
	}
	var got byte
	for i := 1; i < star; i++ {
		got ^= sentence[i]
	}
	return byte(want) == got
}

// parseNMEACoord converts NMEA's DDMM.MMMM / DDDMM.MMMM format (degrees
// then minutes, degree-digit count varies by field — 2 for latitude, 3
// for longitude) plus a hemisphere letter into signed decimal degrees.
func parseNMEACoord(raw, hemisphere string) (float64, error) {
	dot := strings.IndexByte(raw, '.')
	if dot < 2 {
		return 0, fmt.Errorf("malformed NMEA coordinate %q", raw)
	}
	degDigits := dot - 2
	deg, err := strconv.ParseFloat(raw[:degDigits], 64)
	if err != nil {
		return 0, fmt.Errorf("malformed NMEA coordinate degrees %q: %w", raw, err)
	}
	minutes, err := strconv.ParseFloat(raw[degDigits:], 64)
	if err != nil {
		return 0, fmt.Errorf("malformed NMEA coordinate minutes %q: %w", raw, err)
	}
	value := deg + minutes/60
	if hemisphere == "S" || hemisphere == "W" {
		value = -value
	}
	return value, nil
}

// parseGPRMC extracts position and speed from an RMC sentence's fields
// (already split on comma, fields[0] is the sentence ID e.g. "GPRMC").
// valid is false (with a nil error) for a sentence with no fix yet
// (status "V") — that's normal while waiting for one, not a parse failure.
func parseGPRMC(fields []string) (lat, lon, speedKMH float64, valid bool, err error) {
	if len(fields) < 8 {
		return 0, 0, 0, false, fmt.Errorf("short RMC sentence")
	}
	if fields[2] != "A" {
		return 0, 0, 0, false, nil
	}
	lat, err = parseNMEACoord(fields[3], fields[4])
	if err != nil {
		return 0, 0, 0, false, err
	}
	lon, err = parseNMEACoord(fields[5], fields[6])
	if err != nil {
		return 0, 0, 0, false, err
	}
	knots, err := strconv.ParseFloat(fields[7], 64)
	if err != nil {
		return 0, 0, 0, false, err
	}
	return lat, lon, knots * 1.852, true, nil
}

// parseGPGGA extracts position, altitude, and fix quality from a GGA
// sentence's fields. quality 0 means no fix — reported back so the caller
// can treat it the same as an RMC void status, not as a parse error.
func parseGPGGA(fields []string) (lat, lon, altitudeM float64, quality int, err error) {
	if len(fields) < 10 {
		return 0, 0, 0, 0, fmt.Errorf("short GGA sentence")
	}
	quality, err = strconv.Atoi(fields[6])
	if err != nil {
		return 0, 0, 0, 0, err
	}
	if quality == 0 {
		return 0, 0, 0, 0, nil
	}
	lat, err = parseNMEACoord(fields[2], fields[3])
	if err != nil {
		return 0, 0, 0, quality, err
	}
	lon, err = parseNMEACoord(fields[4], fields[5])
	if err != nil {
		return 0, 0, 0, quality, err
	}
	altitudeM, err = strconv.ParseFloat(fields[9], 64)
	if err != nil {
		return 0, 0, 0, quality, err
	}
	return lat, lon, altitudeM, quality, nil
}
