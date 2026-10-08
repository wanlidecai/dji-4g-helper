package main

import (
	"strings"
	"testing"
	"time"
)

func TestVoicePlanOnlyReadsAndPreservesDeviceAndOtherUSBFunctions(t *testing.T) {
	var commands []string
	responses := map[string]string{
		"AT+CGMR":                 "AT+CGMR\r\nQDC507GLEFM21\r\nOK",
		`AT+QCFG="usbcfg"`:        `+QCFG: "usbcfg",0x2CA3,0x4006,1,1,1,1,1,0,0` + "\r\nOK",
		`AT+QCFG="ims"`:           `+QCFG: "ims",0,1` + "\r\nOK",
		`AT+QCFG="volte_disable"`: `+QCFG: "volte_disable",0` + "\r\nOK",
	}
	a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
		commands = append(commands, command)
		return responses[command], nil
	}}
	plan := a.inspectVoiceReadinessPlan()
	if plan.State != "requires_setup" || !plan.ReadOnly || plan.AudioAvailable || plan.ActivationImplemented || len(plan.Errors) > 0 {
		t.Fatalf("plan %+v", plan)
	}
	if strings.Join(commands, "|") != `AT+CGMR|AT+QCFG="usbcfg"|AT+QCFG="ims"|AT+QCFG="volte_disable"` {
		t.Fatalf("nonreadonlyquery %+v", commands)
	}
	if plan.ProposedUSB.VendorID != "2ca3" || plan.ProposedUSB.ProductID != "4006" || !plan.ProposedUSB.ADB || !plan.ProposedUSB.UAC {
		t.Fatalf("proposed %+v", plan.ProposedUSB)
	}
	for i := 0; i < 5; i++ {
		if plan.ProposedUSB.Flags[i] != plan.CurrentUSB.Flags[i] {
			t.Fatal("changed unrelatedUSBfunction")
		}
	}
	if len(plan.ProposedChanges) != 3 || len(plan.Requires) < 5 {
		t.Fatalf("reviewsteps %+v %+v", plan.ProposedChanges, plan.Requires)
	}
}

func TestVoicePlanUnknownConfigurationBlocksAllProposedChanges(t *testing.T) {
	for _, bad := range []string{
		`+QCFG: "usbcfg",0x2CA3,0x4006,1,1,1,1,1,0` + "\r\nOK",
		`+QCFG: "usbcfg",0x2C7C,0x0125,1,1,1,1,1,0,0` + "\r\nOK",
		`+QCFG: "usbcfg",0x2CA3,0x4006,1,1,1,1,2,0,0` + "\r\nOK",
		"+CME ERROR: 30\r\nOK",
	} {
		a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
			switch command {
			case "AT+CGMR":
				return "QDC507GLEFM21\r\nOK", nil
			case `AT+QCFG="usbcfg"`:
				return bad, nil
			case `AT+QCFG="ims"`:
				return `+QCFG: "ims",0,1` + "\r\nOK", nil
			default:
				return `+QCFG: "volte_disable",0` + "\r\nOK", nil
			}
		}}
		plan := a.inspectVoiceReadinessPlan()
		if plan.State != "unsupported" || plan.ProposedUSB != nil || len(plan.ProposedChanges) > 0 {
			t.Fatalf("unsafeplan %+v", plan)
		}
	}
}

func TestVoicePlanAcceptsActualQDC507VoLTESlashAlias(t *testing.T) {
	a := &app{callATOverride: func(command string, _ time.Duration) (string, error) {
		switch command {
		case "AT+CGMR":
			return "QDC507GLEFM21\r\nOK", nil
		case `AT+QCFG="usbcfg"`:
			return `+QCFG: "usbcfg",0x2CA3,0x4006,1,1,1,1,1,0,0` + "\r\nOK", nil
		case `AT+QCFG="ims"`:
			return `+QCFG: "ims",0,1` + "\r\nOK", nil
		case `AT+QCFG="volte_disable"`:
			return `+QCFG: "volte/disable",0` + "\r\nOK", nil
		default:
			t.Fatalf("unexpected command%s", command)
			return "", nil
		}
	}}
	plan := a.inspectVoiceReadinessPlan()
	if plan.State != "requires_setup" || len(plan.Errors) != 0 || plan.VoLTEDisabled == nil || *plan.VoLTEDisabled != 0 {
		t.Fatalf("actual QDC507 alias rejected %+v", plan)
	}
	if _, err := parseVoiceBooleanFields(`+QCFG: "volte/other",0`+"\r\nOK", "volte_disable", 1); err == nil {
		t.Fatal("unrecognizedaliasaccepted")
	}
}
