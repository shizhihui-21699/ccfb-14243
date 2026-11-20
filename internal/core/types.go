// Package core 定义话费欺诈实时监测引擎的领域模型。
package core

import (
	"fmt"
	"time"
)

// Role 分机角色,不同角色使用不同的基线阈值。
// 客服中心坐席外呼频率天然很高,不能和普通员工用同一把尺子。
type Role int

const (
	RoleStaff      Role = iota // 普通员工
	RoleCallCenter             // 客服中心坐席
	RoleExec                   // 管理层(国际通话较多)
)

// Via 呼叫路径。攻击者常利用呼叫转移和语音信箱回拨绕过按分机的限制,
// 因此引擎把"非直接拨出"本身作为一个风险信号。
type Via int

const (
	ViaDirect            Via = iota
	ViaForward               // 呼叫转移产生的出局腿
	ViaVoicemailCallback     // 语音信箱回拨产生的出局腿
)

func (v Via) String() string {
	switch v {
	case ViaForward:
		return "call-forward"
	case ViaVoicemailCallback:
		return "voicemail-callback"
	default:
		return "direct"
	}
}

type EventType int

const (
	EvStart EventType = iota // 呼叫建立
	EvEnd                    // 呼叫挂断
)

// Event 是 PBX/计费系统推送的通话事件。
type Event struct {
	Type   EventType
	CallID string
	Ext    string // 发起(或被转移/被回拨)的分机
	Dst    string // 被叫号码
	Time   time.Time
	Via    Via
	Dur    time.Duration // 仅 EvEnd 有效
}

// RestrictLevel 限制级别。
type RestrictLevel int

const (
	RestrictNone RestrictLevel = iota
	RestrictIntl               // 禁止高风险/国际方向
	RestrictAll                // 禁止全部出局呼叫
)

func (l RestrictLevel) String() string {
	switch l {
	case RestrictIntl:
		return "限制国际/高风险方向"
	case RestrictAll:
		return "全部限制"
	default:
		return "无"
	}
}

// Action 是引擎采取的一次限制动作(也是告警)。
type Action struct {
	Time   time.Time
	Ext    string // 空串表示全局动作(熔断)
	Level  RestrictLevel
	Score  float64
	Reason string
}

func (a Action) String() string {
	target := a.Ext
	if target == "" {
		target = "全局"
	}
	return fmt.Sprintf("[%s] %s %s (score=%.0f): %s",
		a.Time.Format("01-02 15:04:05"), target, a.Level, a.Score, a.Reason)
}

// Decision 是引擎对一次呼叫建立请求的实时裁决。
type Decision struct {
	Allowed bool
}
