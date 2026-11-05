package computeruse

import "encoding/binary"

// x11CoreEvent serializes the common 32-byte KeyPress/KeyRelease and
// ButtonPress/ButtonRelease layout used by the little-endian Linux backends.
func x11CoreEvent(kind, detail uint8, root, window uint32, rootX, rootY, x, y int16, state uint16) [32]byte {
	var event [32]byte
	event[0], event[1] = kind, detail
	binary.LittleEndian.PutUint32(event[8:12], root)
	binary.LittleEndian.PutUint32(event[12:16], window)
	binary.LittleEndian.PutUint16(event[20:22], uint16(rootX))
	binary.LittleEndian.PutUint16(event[22:24], uint16(rootY))
	binary.LittleEndian.PutUint16(event[24:26], uint16(x))
	binary.LittleEndian.PutUint16(event[26:28], uint16(y))
	binary.LittleEndian.PutUint16(event[28:30], state)
	event[30] = 1 // same_screen
	return event
}
