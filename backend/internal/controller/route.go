// Known command8 route fields recovered from Form1.ManipulateTargetInfo.
// Decodes a status1 route response, not Windows session authentication.
package controller

import (
	"errors"
	"strconv"
	"strings"
)

type targetRoute struct {
	lanIP               string
	publicIP            string
	publicPort          int
	lanPort             int
	commandPort         int
	computerNameEncoded string
	tcpPort             int
	tcpServerIP         string
	matchedCustomPass   int
	targetSessionID     int
	tcpServerHostname   string
	partnerSecretToken  string
}

// decodeTargetRoute decodes a status1 command8 route response.
func decodeTargetRoute(payload []byte, codec string) (targetRoute, error) {
	var r targetRoute
	text, err := ansiDecode(payload, codec)
	if err != nil {
		return r, err
	}
	fields := strings.Split(text, ":")
	if len(fields) < 15 || fields[0] != "8" || fields[1] != "1" {
		return r, errors.New("expected status1 command8 route response")
	}
	integer := func(index, low, high int) (int, error) {
		value := fields[index]
		trimmed := strings.TrimPrefix(value, "-")
		if trimmed == "" || !isDecimal(trimmed) {
			return 0, errors.New("invalid numeric route field")
		}
		result, err := strconv.Atoi(value)
		if err != nil || result < low || result > high {
			return 0, errors.New("route field outside modeled range")
		}
		return result, nil
	}
	if r.publicPort, err = integer(5, 0, 65535); err != nil {
		return r, err
	}
	if r.lanPort, err = integer(6, 0, 65535); err != nil {
		return r, err
	}
	if r.commandPort, err = integer(7, 0, 65535); err != nil {
		return r, err
	}
	if r.tcpPort, err = integer(9, 0, 65535); err != nil {
		return r, err
	}
	if r.matchedCustomPass, err = integer(12, -32768, 32767); err != nil {
		return r, err
	}
	if r.targetSessionID, err = integer(13, -2147483648, 2147483647); err != nil {
		return r, err
	}
	r.lanIP = fields[2]
	r.publicIP = fields[4]
	r.computerNameEncoded = fields[8]
	r.tcpServerIP = fields[11]
	r.tcpServerHostname = fields[14]
	if len(fields) > 20 {
		r.partnerSecretToken = fields[20]
	}
	return r, nil
}
