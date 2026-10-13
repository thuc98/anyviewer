// ASCII input commands recovered from RC40 e3a and b66 (E56–E59).
// Coordinates are remote desktop pixels; keys are Windows virtual-key codes.
package controller

import (
	"errors"
	"strconv"
)

func integerInRange(value, low, high int) (string, error) {
	if value < low || value > high {
		return "", errors.New("input value outside supported range")
	}
	return strconv.Itoa(value), nil
}

func mouseMove(x, y int) ([]byte, error) {
	sx, err := integerInRange(x, -32768, 32767)
	if err != nil {
		return nil, err
	}
	sy, err := integerInRange(y, -32768, 32767)
	if err != nil {
		return nil, err
	}
	return []byte("8|||512|||0|||" + sx + "|||" + sy), nil
}

func mouseButton(button string, down bool) ([]byte, error) {
	events := map[string][2]int{
		"left":   {513, 514},
		"right":  {516, 517},
		"middle": {519, 520},
	}
	pair, ok := events[button]
	if !ok {
		return nil, errors.New("unsupported mouse button or state")
	}
	idx := 1
	if down {
		idx = 0
	}
	return []byte("8|||" + strconv.Itoa(pair[idx])), nil
}

func mouseWheel(delta int) ([]byte, error) {
	s, err := integerInRange(delta, -32768, 32767)
	if err != nil {
		return nil, err
	}
	// b6a extracts the signed high word of the original packed wParam.
	n, _ := strconv.Atoi(s)
	return []byte("8|||522|||" + strconv.Itoa(n<<16)), nil
}

func keyboard(vk int, down, system bool) ([]byte, error) {
	var event int
	switch {
	case system && down:
		event = 260
	case system && !down:
		event = 261
	case !system && down:
		event = 256
	default:
		event = 257
	}
	svk, err := integerInRange(vk, 0, 255)
	if err != nil {
		return nil, err
	}
	return []byte("8|||" + strconv.Itoa(event) + "|||" + svk), nil
}
