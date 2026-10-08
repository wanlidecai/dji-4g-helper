package modem

import (
	"reflect"
	"testing"
	"time"
)

func TestCheckAllSMSDeletesOnlyDurablyCachedListedIndices(t *testing.T) {
	m := newRunningTestManager(t)
	validPDU := "079144872000302320048102020000625061028204401AD9775D0E72D7DBE2B21C949E8360B75A4E7683D16AB71B"
	cached := false
	m.SetSMSCallback(func(_, content string, _ time.Time) {
		if content != "" {
			cached = true
		}
	})
	m.SetSMSCacheAcknowledgement(func() bool { return cached })
	done := make(chan []string, 1)
	go func() {
		done <- respondToCommands(t, m, 2, func(req commandRequest) {
			if req.cmd == "AT+CMGL=4" {
				req.respChan <- "+CMGL: 7,0,,38\r\n" + validPDU + "\r\nOK"
				return
			}
			req.respChan <- "OK"
		})
	}()
	m.CheckAllSMS()
	got := <-done
	if !reflect.DeepEqual(got, []string{"AT+CMGL=4", "AT+CMGD=7"}) {
		t.Fatalf("commands%+v", got)
	}
	if !cached {
		t.Fatal("delete occurred beforecacheack")
	}
}

func TestCheckAllSMSRetainsModuleMessageWhenCacheWriteFailed(t *testing.T) {
	m := newRunningTestManager(t)
	validPDU := "079144872000302320048102020000625061028204401AD9775D0E72D7DBE2B21C949E8360B75A4E7683D16AB71B"
	m.SetSMSCallback(func(_, _ string, _ time.Time) {})
	m.SetSMSCacheAcknowledgement(func() bool { return false })
	done := make(chan []string, 1)
	go func() {
		done <- respondToCommands(t, m, 1, func(req commandRequest) { req.respChan <- "+CMGL: 7,0,,38\r\n" + validPDU + "\r\nOK" })
	}()
	m.CheckAllSMS()
	got := <-done
	if !reflect.DeepEqual(got, []string{"AT+CMGL=4"}) {
		t.Fatalf("commands%+v", got)
	}
	select {
	case req := <-m.cmdChan:
		t.Fatalf("failedcache triggeredcleanup%s", req.cmd)
	default:
	}
}
