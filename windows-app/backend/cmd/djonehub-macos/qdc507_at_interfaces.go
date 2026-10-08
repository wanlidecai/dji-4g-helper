package main

// These are the verified AT and Modem interfaces of Baiwang QDC507 2ca3:4006.
// Diagnostic, NMEA, networking and potential audio endpoints are never probed.
func qdc507ATInterface(number int) bool { return number == 2 || number == 3 }
