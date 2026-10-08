package main

import "encoding/binary"

// MIB_TCPTABLE_OWNER_PID has a DWORD count, then six-DWORD rows. IPv4 and
// port fields are in network byte order; the PID is a native little-endian DWORD.
func tcpListenerTableHasOwner(buffer []byte, pid uint32, port uint16) bool {
	if len(buffer) < 4 {
		return false
	}
	rows := int(binary.LittleEndian.Uint32(buffer[:4]))
	if rows > (len(buffer)-4)/24 {
		return false
	}
	for index := 0; index < rows; index++ {
		row := buffer[4+index*24 : 4+(index+1)*24]
		if binary.LittleEndian.Uint32(row[:4]) != 2 {
			continue
		}
		if row[4] == 127 && row[5] == 0 && row[6] == 0 && row[7] == 1 &&
			binary.BigEndian.Uint16(row[8:10]) == port && binary.LittleEndian.Uint32(row[20:24]) == pid {
			return true
		}
	}
	return false
}
