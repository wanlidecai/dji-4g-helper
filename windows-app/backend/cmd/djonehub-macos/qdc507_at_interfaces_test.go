package main

import "testing"

func TestQDC507ATInterfacesExcludeDiagnosticAndNetwork(t *testing.T) {
	for _, number := range []int{-1, 0, 1, 4, 5, 6, 7, 8} {
		if qdc507ATInterface(number) {
			t.Fatalf("unsafe AT interface %d allowed", number)
		}
	}
	for _, number := range []int{2, 3} {
		if !qdc507ATInterface(number) {
			t.Fatalf("verified AT interface %d excluded", number)
		}
	}
}
