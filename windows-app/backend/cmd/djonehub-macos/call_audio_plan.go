package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type voiceUSBComposition struct {
	VendorID  string `json:"vendor_id"`
	ProductID string `json:"product_id"`
	Flags     []int  `json:"flags"`
	ADB       bool   `json:"adb"`
	UAC       bool   `json:"uac"`
}

type voiceReadinessPlan struct {
	State                 string               `json:"state"`
	Summary               string               `json:"summary"`
	ReadOnly              bool                 `json:"read_only"`
	AudioAvailable        bool                 `json:"audio_available"`
	ActivationImplemented bool                 `json:"activation_implemented"`
	Firmware              string               `json:"firmware,omitempty"`
	CurrentUSB            *voiceUSBComposition `json:"current_usb,omitempty"`
	ProposedUSB           *voiceUSBComposition `json:"proposed_usb,omitempty"`
	IMS                   *int                 `json:"ims,omitempty"`
	VoLTECapability       *int                 `json:"volte_capability,omitempty"`
	VoLTEDisabled         *int                 `json:"volte_disabled,omitempty"`
	ProposedChanges       []string             `json:"proposed_changes"`
	Requires              []string             `json:"requires"`
	Errors                map[string]string    `json:"errors,omitempty"`
	UpdatedAt             time.Time            `json:"updated_at"`
}

var voiceConfigLine = regexp.MustCompile(`(?im)^\s*\+QCFG:\s*"([^"]+)",\s*([^\r\n]+)`)

func parseVoiceConfigFields(response, name string) ([]string, error) {
	if !callQuerySucceeded(response) {
		return nil, fmt.Errorf("模块未接受配置读取")
	}
	for _, match := range voiceConfigLine.FindAllStringSubmatch(response, -1) {
		matchesName := strings.EqualFold(match[1], name) ||
			(name == "volte_disable" && strings.EqualFold(match[1], "volte/disable"))
		if !matchesName {
			continue
		}

		reader := csv.NewReader(strings.NewReader(match[2]))
		reader.TrimLeadingSpace = true
		fields, err := reader.Read()
		if err != nil {
			return nil, err
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		return fields, nil
	}
	return nil, fmt.Errorf("模块未返回 %s 配置", name)
}

func parseVoiceUSBComposition(response string) (*voiceUSBComposition, error) {
	fields, err := parseVoiceConfigFields(response, "usbcfg")
	if err != nil {
		return nil, err
	}
	if len(fields) != 9 {
		return nil, fmt.Errorf("USB 功能位数量未知，不能安全制定修改计划")
	}
	vendor, err := strconv.ParseUint(fields[0], 0, 16)
	if err != nil {
		return nil, fmt.Errorf("USB VID 无效")
	}
	product, err := strconv.ParseUint(fields[1], 0, 16)
	if err != nil {
		return nil, fmt.Errorf("USB PID 无效")
	}
	if vendor != 0x2ca3 || product != 0x4006 {
		return nil, fmt.Errorf("USB 身份不是已验证的 Baiwang QDC507 2ca3:4006")
	}
	composition := &voiceUSBComposition{VendorID: fmt.Sprintf("%04x", vendor), ProductID: fmt.Sprintf("%04x", product), Flags: make([]int, 7)}
	for i, field := range fields[2:] {
		value, err := strconv.Atoi(field)
		if err != nil || (value != 0 && value != 1) {
			return nil, fmt.Errorf("USB 配置包含未知功能值")
		}
		composition.Flags[i] = value
	}
	composition.ADB, composition.UAC = composition.Flags[5] == 1, composition.Flags[6] == 1
	return composition, nil
}

func parseVoiceBooleanFields(response, name string, count int) ([]int, error) {
	fields, err := parseVoiceConfigFields(response, name)
	if err != nil {
		return nil, err
	}
	if len(fields) != count {
		return nil, fmt.Errorf("%s 配置结构未知", name)
	}
	values := make([]int, count)
	for i, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil || (value != 0 && value != 1) {
			return nil, fmt.Errorf("%s 配置值未知", name)
		}
		values[i] = value
	}
	return values, nil
}

func parseVoiceFirmware(response string) string {
	if !callQuerySucceeded(response) {
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "+CGMR:"))
		if strings.HasPrefix(strings.ToUpper(line), "QDC507") && len(line) <= 100 {
			return line
		}
	}
	return ""
}

func (a *app) inspectVoiceReadinessPlan() voiceReadinessPlan {
	plan := voiceReadinessPlan{State: "unsupported", Summary: "电脑通话音频需要单独配置", ReadOnly: true,
		ProposedChanges: []string{}, Requires: []string{}, Errors: map[string]string{}, UpdatedAt: time.Now()}
	responses := make(map[string]string)
	for _, query := range []struct{ name, command string }{
		{"firmware", "AT+CGMR"}, {"usb", `AT+QCFG="usbcfg"`}, {"ims", `AT+QCFG="ims"`}, {"volte", `AT+QCFG="volte_disable"`},
	} {
		response, err := a.callCommand(query.command, 4*time.Second)
		if err != nil {
			plan.Errors[query.name] = "模块读取暂不可用"
			continue
		}
		responses[query.name] = response
	}
	plan.Firmware = parseVoiceFirmware(responses["firmware"])
	if plan.Firmware == "" {
		plan.Errors["firmware"] = "尚未确认 QDC507 固件身份"
	}
	composition, err := parseVoiceUSBComposition(responses["usb"])
	if err != nil {
		plan.Errors["usb"] = err.Error()
	} else {
		plan.CurrentUSB = composition
	}
	ims, err := parseVoiceBooleanFields(responses["ims"], "ims", 2)
	if err != nil {
		plan.Errors["ims"] = err.Error()
	} else {
		plan.IMS = &ims[0]
		plan.VoLTECapability = &ims[1]
	}
	volte, err := parseVoiceBooleanFields(responses["volte"], "volte_disable", 1)
	if err != nil {
		plan.Errors["volte"] = err.Error()
	} else {
		plan.VoLTEDisabled = &volte[0]
	}
	if len(plan.Errors) > 0 {
		plan.Summary = "配置或模块身份尚未确认，不能制定音频启用计划"
		return plan
	}
	if *plan.VoLTECapability != 1 {
		plan.Summary = "模块未报告 VoLTE 能力，暂不能启用电脑通话音频"
		return plan
	}
	plan.State = "requires_setup"
	plan.Summary = "本版提供通话控制；电脑麦克风和扬声器的通话音频尚未实现"
	proposed := *composition
	proposed.Flags = append([]int(nil), composition.Flags...)
	proposed.Flags[5], proposed.Flags[6] = 1, 1
	proposed.ADB, proposed.UAC = true, true
	plan.ProposedUSB = &proposed
	if !composition.ADB {
		plan.ProposedChanges = append(plan.ProposedChanges, "在保留 USB VID/PID 和其他功能位的前提下开启 ADB")
	}
	if !composition.UAC {
		plan.ProposedChanges = append(plan.ProposedChanges, "开启模块 USB 音频接口（UAC）")
	}
	if *plan.IMS != 1 {
		plan.ProposedChanges = append(plan.ProposedChanges, "将 IMS 从 0 调整为 1")
	}
	if *plan.VoLTEDisabled != 0 {
		plan.ProposedChanges = append(plan.ProposedChanges, "启用模块 VoLTE")
	}
	plan.Requires = []string{
		"确认模块 ADB 持久授权；配置备份无法保证撤销该授权",
		"修改前保存与模块身份匹配的 USB 和 IMS/VoLTE 备份",
		"受控重启并核对同一模块；期间网络会短暂中断",
		"确认模块内核为 3.18.44 后，核验并加载匹配的语音运行时",
		"接入本机麦克风和扬声器并授权麦克风访问",
		"使用另一部电话验证实际双向可听；接口就绪不等于通话验收通过",
	}
	return plan
}

func (a *app) voiceReadinessPlan(w http.ResponseWriter, _ *http.Request) {
	// No configuration writes, ADB authentication, driver download/load,
	// module reboot or microphone capture is performed by this endpoint.
	writeJSON(w, http.StatusOK, a.inspectVoiceReadinessPlan())
}
