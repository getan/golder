package computeruse

import (
	"encoding/binary"
	"testing"
)

func TestX11CoreEventUsesExactDestinationAndCoordinates(t *testing.T) {
	event := x11CoreEvent(4, 1, 100, 200, -10, 20, 30, 40, 1<<8)
	if event[0] != 4 || event[1] != 1 || binary.LittleEndian.Uint32(event[8:12]) != 100 || binary.LittleEndian.Uint32(event[12:16]) != 200 {
		t.Fatalf("wrong X11 event destination: %v", event)
	}
	if int16(binary.LittleEndian.Uint16(event[20:22])) != -10 || binary.LittleEndian.Uint16(event[22:24]) != 20 || binary.LittleEndian.Uint16(event[24:26]) != 30 || binary.LittleEndian.Uint16(event[26:28]) != 40 {
		t.Fatalf("wrong X11 event coordinates: %v", event)
	}
	if binary.LittleEndian.Uint16(event[28:30]) != 1<<8 || event[30] != 1 || event[31] != 0 {
		t.Fatalf("wrong X11 event state: %v", event)
	}
}
