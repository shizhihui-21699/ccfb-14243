package core

import (
	"fmt"
	"strings"
	"time"
)

// EngineConfig 引擎阈值。默认值按"普通企业 200 分机 + 30 坐席"的规模标定。
type EngineConfig struct {
	IntlBlockScore   float64       // 评分达到即限制国际/高风险方向
	FullBlockScore   float64       // 评分达到即全部限制
	RestrictDuration time.Duration // 限制时长(到期自动解除,再次触发再限制)
	GlobalSpendLimit float64       // 全局高风险话费熔断(元/滚动小时)
	StaffSpendLimit  float64       // 普通分机高风险话费阈值(元/滚动小时)
	CCSpendLimit     float64
	ExecSpendLimit   float64
	Holidays         map[string]bool // 节假日历 "2006-01-02"
}

func DefaultConfig() EngineConfig {
	return EngineConfig{
		IntlBlockScore:   50,
		FullBlockScore:   85,
		RestrictDuration: 30 * time.Minute,
		GlobalSpendLimit: 400,
		StaffSpendLimit:  50,
		CCSpendLimit:     200,
		ExecSpendLimit:   300,
		Holidays:         map[string]bool{},
	}
}

type spendRec struct {
	t   time.Time
	amt float64
}

type activeCall struct {
	start time.Time
	rate  float64
	risky bool // 建立时风险 >= 0.15,计入话费统计
}

type extState struct {
	role       Role
	active     map[string]activeCall
	starts     []time.Time // 近期呼叫建立时间(滑动窗口)
	spend      []spendRec  // 已完结呼叫的高风险话费
	restricted RestrictLevel
	until      time.Time
	hourly     [24]float64 // 分时段呼叫频次基线(EWMA)
}

// Engine 实时欺诈检测引擎。纯事件驱动、无后台协程:
// 呼叫建立时同步评分并裁决(允许/拒绝),挂断时累计话费。
type Engine struct {
	cfg             EngineConfig
	risk            *RiskTable
	exts            map[string]*extState
	globalSpend     []spendRec
	countryExts     map[string]map[string]time.Time // 方向 -> 近期呼过的分机(识别分布式盗打)
	globalIntlUntil time.Time                       // 全局熔断截止时间
}

func NewEngine(cfg EngineConfig, risk *RiskTable) *Engine {
	return &Engine{
		cfg:         cfg,
		risk:        risk,
		exts:        map[string]*extState{},
		countryExts: map[string]map[string]time.Time{},
	}
}

func (e *Engine) SetRole(ext string, r Role) { e.ext(ext).role = r }

func (e *Engine) ext(id string) *extState {
	st, ok := e.exts[id]
	if !ok {
		st = &extState{active: map[string]activeCall{}}
		e.exts[id] = st
	}
	return st
}

// Process 处理一个事件,返回对呼叫建立的裁决和本次触发的限制动作。
func (e *Engine) Process(ev Event) (Decision, []Action) {
	if ev.Type == EvEnd {
		e.onEnd(ev)
		return Decision{Allowed: true}, nil
	}
	return e.onStart(ev)
}

func (e *Engine) onStart(ev Event) (Decision, []Action) {
	st := e.ext(ev.Ext)
	now := ev.Time
	risk, rate := e.risk.Lookup(ev.Dst)
	risky := risk >= 0.3

	// 限制到期自动解除
	if st.restricted != RestrictNone && !st.until.After(now) {
		st.restricted = RestrictNone
	}

	// 1) 已有限制 / 全局熔断直接拦截
	if st.restricted == RestrictAll ||
		(st.restricted == RestrictIntl && risky) ||
		(risky && e.globalIntlUntil.After(now) && st.role != RoleCallCenter) {
		return Decision{Allowed: false}, nil
	}

	// 2) 多信号评分
	s := 0.0
	var why []string
	add := func(p float64, format string, args ...any) {
		s += p
		why = append(why, fmt.Sprintf(format, args...))
	}

	if risk > 0 {
		add(45*risk, "目的地风险%.2f", risk)
	}
	if ev.Via != ViaDirect && risk > 0.2 {
		add(25, "经%s呼出", ev.Via)
	}
	if e.offHours(now) && risk > 0.2 {
		add(15, "非工作时间")
	}
	hour := now.Hour()
	if st.hourly[hour] < 0.5 && risk > 0.2 {
		add(10, "该分机此时段无呼叫习惯")
	}
	conc := len(st.active) + 1
	allowedConc := 1
	switch st.role {
	case RoleExec:
		allowedConc = 2
	case RoleCallCenter:
		allowedConc = 4 // 预测式外呼:坐席通话中系统已在拨下一通
	}
	// 并发信号只在目的地有风险时计入:国内低价方向的话务洪峰损失有限,
	// 客服集中外呼、总机转接都会造成正常的多路并发。
	if conc > allowedConc && risk > 0.1 {
		add(18*float64(conc-allowedConc), "并发%d路", conc)
	}
	st.starts = append(st.starts, now)
	st.starts = pruneTimes(st.starts, now.Add(-10*time.Minute))
	if vel := len(st.starts); vel > e.velLimit(st.role) {
		p := 15 + 2*float64(vel-e.velLimit(st.role))
		if p > 40 {
			p = 40
		}
		add(p, "10分钟呼叫%d次", vel)
	}
	st.spend = pruneSpend(st.spend, now.Add(-time.Hour))
	spend1h := sumSpend(st.spend) + accrued(st.active, now)
	if spend1h > e.spendLimit(st.role) {
		add(25, "1小时高风险话费%.0f元", spend1h)
	}
	if risk > 0.4 {
		c := countryOf(ev.Dst)
		set := e.countryExts[c]
		if set == nil {
			set = map[string]time.Time{}
			e.countryExts[c] = set
		}
		for x, t := range set { // 15 分钟窗口
			if now.Sub(t) > 15*time.Minute {
				delete(set, x)
			}
		}
		set[ev.Ext] = now
		if len(set) >= 4 {
			add(25, "方向%s被%d个分机集中呼叫", c, len(set))
		}
	}

	// 3) 按评分升级限制
	var acts []Action
	lvl := RestrictNone
	if s >= e.cfg.FullBlockScore {
		lvl = RestrictAll
	} else if s >= e.cfg.IntlBlockScore {
		lvl = RestrictIntl
	}
	if lvl != RestrictNone && lvl > st.restricted {
		st.restricted = lvl
		st.until = now.Add(e.cfg.RestrictDuration)
		a := Action{Time: now, Ext: ev.Ext, Level: lvl, Score: s, Reason: strings.Join(why, "; ")}
		acts = append(acts, a)
	}

	// 4) 全局熔断:所有分机的高风险话费合计超限 -> 临时切断全局高风险方向
	//    (客服坐席豁免,避免误伤正常外呼业务)
	e.globalSpend = pruneSpend(e.globalSpend, now.Add(-time.Hour))
	g := sumSpend(e.globalSpend) + e.globalAccrued(now)
	if g > e.cfg.GlobalSpendLimit && !e.globalIntlUntil.After(now) {
		e.globalIntlUntil = now.Add(e.cfg.RestrictDuration)
		acts = append(acts, Action{
			Time: now, Ext: "", Level: RestrictIntl, Score: g,
			Reason: fmt.Sprintf("全局1小时高风险话费%.0f元超过熔断线%.0f元", g, e.cfg.GlobalSpendLimit),
		})
	}

	// 5) 裁决本次呼叫
	if lvl == RestrictAll || (lvl == RestrictIntl && risky) ||
		(risky && e.globalIntlUntil.After(now) && st.role != RoleCallCenter) {
		return Decision{Allowed: false}, acts
	}

	st.active[ev.CallID] = activeCall{start: now, rate: rate, risky: risk >= 0.15}
	st.hourly[hour] = 0.9*st.hourly[hour] + 0.1
	return Decision{Allowed: true}, acts
}

func (e *Engine) onEnd(ev Event) {
	st, ok := e.exts[ev.Ext]
	if !ok {
		return
	}
	ac, ok := st.active[ev.CallID]
	if !ok {
		return
	}
	delete(st.active, ev.CallID)
	if ac.risky {
		cost := ac.rate * ev.Dur.Minutes()
		st.spend = append(st.spend, spendRec{ev.Time, cost})
		e.globalSpend = append(e.globalSpend, spendRec{ev.Time, cost})
	}
}

// offHours 非工作时间:夜间(23:00-06:00)、周末、节假日。
func (e *Engine) offHours(t time.Time) bool {
	if e.cfg.Holidays[t.Format("2006-01-02")] {
		return true
	}
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return true
	}
	return t.Hour() >= 23 || t.Hour() < 6
}

func (e *Engine) velLimit(r Role) int {
	switch r {
	case RoleCallCenter:
		return 60
	case RoleExec:
		return 10
	default:
		return 6
	}
}

func (e *Engine) spendLimit(r Role) float64 {
	switch r {
	case RoleCallCenter:
		return e.cfg.CCSpendLimit
	case RoleExec:
		return e.cfg.ExecSpendLimit
	default:
		return e.cfg.StaffSpendLimit
	}
}

func (e *Engine) globalAccrued(now time.Time) float64 {
	total := 0.0
	for _, st := range e.exts {
		total += accrued(st.active, now)
	}
	return total
}

// accrued 在途呼叫到 now 为止已产生的高风险话费(实时计费视角)。
func accrued(active map[string]activeCall, now time.Time) float64 {
	total := 0.0
	for _, c := range active {
		if c.risky {
			total += c.rate * now.Sub(c.start).Minutes()
		}
	}
	return total
}

func countryOf(dst string) string {
	if strings.HasPrefix(dst, "00") && len(dst) >= 5 {
		return dst[:5]
	}
	return "domestic"
}

func pruneTimes(ts []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(since) {
		i++
	}
	return ts[i:]
}

func pruneSpend(s []spendRec, since time.Time) []spendRec {
	i := 0
	for i < len(s) && s[i].t.Before(since) {
		i++
	}
	return s[i:]
}

func sumSpend(s []spendRec) float64 {
	t := 0.0
	for _, r := range s {
		t += r.amt
	}
	return t
}
