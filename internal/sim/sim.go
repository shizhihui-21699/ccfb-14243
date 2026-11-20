// Package sim 模拟 PBX 通话事件流:正常业务流量(含出差员工、客服外呼、
// 节假日)和多种盗打手法,并把事件回放给检测引擎,统计发现时间与误限制。
package sim

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"

	"fraudmon/internal/core"
)

// Call 一次完整呼叫(回放时拆成建立/挂断两个事件)。
type Call struct {
	ID       string
	Ext      string
	Dst      string
	Start    time.Time
	Dur      time.Duration
	Via      core.Via
	Attacker bool //  ground truth:是否为盗打呼叫
}

// RiskUpdate 运营商在运行中动态调整号段风险。
type RiskUpdate struct {
	T          time.Time
	Prefix     string
	Risk, Rate float64
}

type Scenario struct {
	Name        string
	Calls       []Call
	Attackers   map[string]bool // ground truth:被控制的分机
	AttackStart time.Time
	Updates     []RiskUpdate
}

// Gen 生成呼叫并分配唯一 CallID。
type Gen struct {
	rnd   *rand.Rand
	calls []Call
	n     int
}

func NewGen(rnd *rand.Rand) *Gen { return &Gen{rnd: rnd} }

func (g *Gen) Calls() []Call { return g.calls }

func (g *Gen) add(ext, dst string, t time.Time, durMin float64, via core.Via, attacker bool) {
	if durMin < 0.3 {
		durMin = 0.3
	}
	g.calls = append(g.calls, Call{
		ID: fmt.Sprintf("c%07d", g.n), Ext: ext, Dst: dst, Start: t,
		Dur: time.Duration(durMin * float64(time.Minute)), Via: via, Attacker: attacker,
	})
	g.n++
}

// Directory 分机表:200 个员工分机、30 个客服坐席、5 个管理层。
func Directory() (staff, cc, execs []string) {
	for i := 0; i < 200; i++ {
		staff = append(staff, fmt.Sprintf("1%03d", i))
	}
	for i := 0; i < 30; i++ {
		cc = append(cc, fmt.Sprintf("2%03d", i))
	}
	for i := 0; i < 5; i++ {
		execs = append(execs, fmt.Sprintf("9%03d", i))
	}
	return
}

func poisson(rnd *rand.Rand, lam float64) int {
	if lam <= 0 {
		return 0
	}
	l := math.Exp(-lam)
	k, p := 0, 1.0
	for p > l {
		k++
		p *= rnd.Float64()
	}
	return k - 1
}

func isWeekend(t time.Time) bool {
	w := t.Weekday()
	return w == time.Saturday || w == time.Sunday
}

// ---- 号码生成 ----

func mobile(rnd *rand.Rand) string   { return fmt.Sprintf("13%09d", rnd.Intn(1000000000)) }
func landline(rnd *rand.Rand) string { return fmt.Sprintf("010%08d", rnd.Intn(100000000)) }
func usNum(rnd *rand.Rand) string    { return fmt.Sprintf("001415%07d", rnd.Intn(10000000)) }
func ukNum(rnd *rand.Rand) string    { return fmt.Sprintf("004420%08d", rnd.Intn(100000000)) }
func hkNum(rnd *rand.Rand) string    { return fmt.Sprintf("00852%08d", rnd.Intn(100000000)) }
func deNum(rnd *rand.Rand) string    { return fmt.Sprintf("004930%08d", rnd.Intn(100000000)) }

func staffDst(rnd *rand.Rand) string {
	switch r := rnd.Float64(); {
	case r < 0.94:
		return mobile(rnd)
	case r < 0.96:
		return landline(rnd)
	case r < 0.98:
		return usNum(rnd) // 正常业务国际长途:美国
	case r < 0.99:
		return ukNum(rnd)
	default:
		return hkNum(rnd)
	}
}

// Background 生成 [start,end) 的正常业务流量:
//   - 员工:工作日白天平均 6 通/天,94% 国内,少量国际;周末/节假日骤减
//   - 出差员工(8 人):每天 2 通左右国际长途,时段延伸到早晚
//   - 管理层:国际通话偏多,晚间也有
//   - 客服中心:工作日 10-12 点、14-18 点集中外呼,每坐席每小时约 10 通
//   - 夜间:少量值班电话
func Background(g *Gen, start, end time.Time, staff, cc, execs []string, holidays map[string]bool) {
	rnd := g.rnd
	travelers := staff[50:58]
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		workday := !isWeekend(d) && !holidays[d.Format("2006-01-02")]
		at := func(hour float64) time.Time {
			return d.Add(time.Duration(hour * float64(time.Hour)))
		}
		lam := 6.0
		if !workday {
			lam = 0.4
		}
		for _, ext := range staff {
			for i, n := 0, poisson(rnd, lam); i < n; i++ {
				t := at(9 + rnd.Float64()*9)
				if !workday {
					t = at(9 + rnd.Float64()*13)
				}
				g.add(ext, staffDst(rnd), t, 3+rnd.NormFloat64()*1.5, core.ViaDirect, false)
			}
		}
		for _, ext := range travelers {
			for i, n := 0, poisson(rnd, 2); i < n; i++ {
				dst := usNum(rnd)
				switch rnd.Intn(3) {
				case 1:
					dst = ukNum(rnd)
				case 2:
					dst = hkNum(rnd)
				}
				g.add(ext, dst, at(8+rnd.Float64()*15), 5+rnd.Float64()*15, core.ViaDirect, false)
			}
		}
		for _, ext := range execs {
			for i, n := 0, poisson(rnd, 2); i < n; i++ {
				dst := mobile(rnd)
				if rnd.Float64() < 0.6 {
					switch rnd.Intn(3) {
					case 0:
						dst = usNum(rnd)
					case 1:
						dst = ukNum(rnd)
					default:
						dst = deNum(rnd)
					}
				}
				g.add(ext, dst, at(9+rnd.Float64()*13), 5+rnd.Float64()*25, core.ViaDirect, false)
			}
		}
		if workday {
			for _, ext := range cc {
				for _, h0 := range []float64{10, 11, 14, 15, 16, 17} {
					for i, n := 0, poisson(rnd, 10); i < n; i++ {
						g.add(ext, mobile(rnd), at(h0+rnd.Float64()), 0.5+rnd.Float64()*3.5, core.ViaDirect, false)
					}
				}
			}
		}
		for i, n := 0, poisson(rnd, 3); i < n; i++ { // 夜间值班
			g.add(staff[rnd.Intn(len(staff))], mobile(rnd), at(23+rnd.Float64()*7), 1+rnd.Float64()*4, core.ViaDirect, false)
		}
	}
}

func extSet(exts []string) map[string]bool {
	m := map[string]bool{}
	for _, e := range exts {
		m[e] = true
	}
	return m
}

// Burst 手法一:凌晨突发。周六 02:00,3 个被猜出口令的分机,
// 30 路并发持续拨打卫星高价号码,一路挂断立刻补一路。
func Burst(g *Gen, exts []string, t0 time.Time) (map[string]bool, time.Time) {
	rnd := g.rnd
	end := t0.Add(8 * time.Hour)
	for i := 0; i < 30; i++ {
		ext := exts[i%len(exts)]
		for t := t0; t.Before(end); {
			dur := 20 + rnd.Float64()*40
			prefix := "00882"
			if rnd.Float64() < 0.4 {
				prefix = "00881"
			}
			g.add(ext, fmt.Sprintf("%s%09d", prefix, rnd.Intn(1000000000)), t, dur, core.ViaDirect, true)
			t = t.Add(time.Duration(dur*float64(time.Minute)) + 5*time.Second)
		}
	}
	return extSet(exts), t0
}

// LowAndSlow 手法二:低慢速。工作时间混入,12 个分机轮换,
// 每分机每小时不超过 2 通,全局并发不超过 3 路,专打索马里高价号。
func LowAndSlow(g *Gen, exts []string, t0, end time.Time) (map[string]bool, time.Time) {
	rnd := g.rnd
	i := 0
	for t := t0; t.Before(end); t = t.Add(210 * time.Second) {
		g.add(exts[i%len(exts)], fmt.Sprintf("00252%09d", rnd.Intn(1000000000)),
			t, 12+rnd.Float64()*6, core.ViaDirect, true)
		i++
	}
	return extSet(exts), t0
}

// Forward 手法三:呼叫转移 + 语音信箱回拨绕过。
//   - 深夜:5 个分机被设置呼叫转移到澳门高价号,外部来电触发出局腿
//   - 午间:同样的转移,且一个分机同时转移多路(转移不受单路并发约束)
//   - 语音信箱回拨:攻击者留言后,系统回拨到 00701 国际号
func Forward(g *Gen, exts []string, nightT0, midT0 time.Time) (map[string]bool, time.Time) {
	rnd := g.rnd
	for i, ext := range exts { // 深夜转移
		for k := 0; k < 2; k++ {
			t := nightT0.Add(time.Duration(i*7+k*3) * time.Minute)
			g.add(ext, fmt.Sprintf("00853%08d", rnd.Intn(100000000)), t, 15+rnd.Float64()*15, core.ViaForward, true)
		}
	}
	for i, ext := range exts { // 午间转移,每分机 3 路并发
		for k := 0; k < 3; k++ {
			t := midT0.Add(time.Duration(i*2) * time.Minute).Add(time.Duration(k*30) * time.Second)
			g.add(ext, fmt.Sprintf("00853%08d", rnd.Intn(100000000)), t, 15+rnd.Float64()*10, core.ViaForward, true)
		}
	}
	for i, ext := range exts { // 语音信箱回拨
		for k := 0; k < 2; k++ {
			t := midT0.Add(time.Hour).Add(time.Duration(i*25+k*20) * time.Minute)
			g.add(ext, fmt.Sprintf("00701%08d", rnd.Intn(100000000)), t, 18+rnd.Float64()*7, core.ViaVoicemailCallback, true)
		}
	}
	return extSet(exts), nightT0
}

// RiskDrift 手法四:利用风险表未覆盖的新号段。
// 00700 段运营商尚未评级(低风险 0.15),攻击者周五晚间中等强度拨打;
// 2 小时后运营商确认其为高价段,风险上调到 0.9。
func RiskDrift(g *Gen, exts []string, t0, end time.Time) (map[string]bool, time.Time) {
	rnd := g.rnd
	for t := t0; t.Before(end); t = t.Add(20 * time.Minute) {
		for _, ext := range exts {
			if rnd.Float64() < 0.5 {
				g.add(ext, fmt.Sprintf("00700%08d", rnd.Intn(100000000)),
					t.Add(time.Duration(rnd.Float64()*5)*time.Minute), 15+rnd.Float64()*10, core.ViaDirect, true)
			}
		}
	}
	return extSet(exts), t0
}

// ---- 回放与统计 ----

type Result struct {
	Scenario       string
	EngineOn       bool
	Detected       bool
	DetectDelay    time.Duration // 从攻击开始到首次限制动作
	LossAtDetect   float64       // 检出时已产生的欺诈话费(含在途)
	FraudCost      float64       // 欺诈话费总额
	FraudCompleted int           // 完成的欺诈呼叫数
	FraudBlocked   int           // 被拦截的欺诈呼叫数
	FalseRestrExts []string      // 被误限制的合法分机
	FalseDenied    int           // 被误拒的合法呼叫数
	LegitCost      float64       // 正常话费(参照)
	Actions        []core.Action
}

// Run 把场景事件按时间序回放。engineOn=false 时不启用引擎,
// 用于估算"无防护损失"基线。
func Run(sc Scenario, roles map[string]core.Role, cfg core.EngineConfig, engineOn bool) Result {
	res := Result{Scenario: sc.Name, EngineOn: engineOn}
	risk := core.NewRiskTable()

	type item struct {
		t    time.Time
		kind int // 0=风险表更新 1=挂断 2=建立(同时刻按此序处理)
		call Call
		upd  RiskUpdate
	}
	items := make([]item, 0, len(sc.Calls)*2+len(sc.Updates))
	for _, u := range sc.Updates {
		items = append(items, item{t: u.T, kind: 0, upd: u})
	}
	for _, c := range sc.Calls {
		items = append(items, item{t: c.Start, kind: 2, call: c})
		items = append(items, item{t: c.Start.Add(c.Dur), kind: 1, call: c})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].t.Equal(items[j].t) {
			return items[i].t.Before(items[j].t)
		}
		return items[i].kind < items[j].kind
	})

	if !engineOn { // 无防护:所有呼叫都完成,只算账
		for _, it := range items {
			switch it.kind {
			case 0:
				risk.Set(it.upd.Prefix, it.upd.Risk, it.upd.Rate)
			case 2:
				_, rate := risk.Lookup(it.call.Dst)
				if it.call.Attacker {
					res.FraudCost += rate * it.call.Dur.Minutes()
					res.FraudCompleted++
				} else {
					res.LegitCost += rate * it.call.Dur.Minutes()
				}
			}
		}
		return res
	}

	eng := core.NewEngine(cfg, risk)
	for ext, r := range roles {
		eng.SetRole(ext, r)
	}
	denied := map[string]bool{}
	rateAt := map[string]float64{}
	type ac struct {
		start time.Time
		rate  float64
	}
	fraudActive := map[string]ac{}
	falseSet := map[string]bool{}

	for _, it := range items {
		switch it.kind {
		case 0:
			risk.Set(it.upd.Prefix, it.upd.Risk, it.upd.Rate)
		case 2:
			c := it.call
			_, rate := risk.Lookup(c.Dst)
			dec, acts := eng.Process(core.Event{
				Type: core.EvStart, CallID: c.ID, Ext: c.Ext, Dst: c.Dst, Time: c.Start, Via: c.Via,
			})
			for _, a := range acts {
				res.Actions = append(res.Actions, a)
				isMitigation := a.Ext == "" || sc.Attackers[a.Ext]
				switch {
				case isMitigation && len(sc.Attackers) > 0 && !res.Detected && !a.Time.Before(sc.AttackStart):
					res.Detected = true
					res.DetectDelay = a.Time.Sub(sc.AttackStart)
					loss := res.FraudCost
					for _, fa := range fraudActive {
						loss += fa.rate * a.Time.Sub(fa.start).Minutes()
					}
					res.LossAtDetect = loss
				case !isMitigation && !falseSet[a.Ext]:
					falseSet[a.Ext] = true
					res.FalseRestrExts = append(res.FalseRestrExts, a.Ext)
				}
			}
			if !dec.Allowed {
				denied[c.ID] = true
				if c.Attacker {
					res.FraudBlocked++
				} else {
					res.FalseDenied++
				}
				continue
			}
			rateAt[c.ID] = rate
			if c.Attacker {
				fraudActive[c.ID] = ac{c.Start, rate}
			}
		case 1:
			c := it.call
			if denied[c.ID] {
				continue
			}
			eng.Process(core.Event{
				Type: core.EvEnd, CallID: c.ID, Ext: c.Ext, Dst: c.Dst,
				Time: c.Start.Add(c.Dur), Dur: c.Dur,
			})
			cost := rateAt[c.ID] * c.Dur.Minutes()
			if c.Attacker {
				res.FraudCost += cost
				res.FraudCompleted++
				delete(fraudActive, c.ID)
			} else {
				res.LegitCost += cost
			}
		}
	}
	return res
}
