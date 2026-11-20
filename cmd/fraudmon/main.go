// fraudmon 话费欺诈实时监测演示:
// 模拟 5 天(含周末和节假日)的通话事件流,叠加 4 种盗打手法,
// 统计每种手法的发现时间、检出时损失、防护下总损失和误限制情况。
package main

import (
	"fmt"
	"math/rand"
	"os"
	"text/tabwriter"
	"time"

	"fraudmon/internal/core"
	"fraudmon/internal/sim"
)

func main() {
	staff, cc, execs := sim.Directory()

	// 2026-11-19(周四) ~ 2026-11-23(周一,节假日),共 5 天
	start := time.Date(2026, 11, 19, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 5)
	holidays := map[string]bool{"2026-11-23": true}

	bg := sim.NewGen(rand.New(rand.NewSource(42)))
	sim.Background(bg, start, end, staff, cc, execs, holidays)
	background := bg.Calls()

	roles := map[string]core.Role{}
	for _, e := range staff {
		roles[e] = core.RoleStaff
	}
	for _, e := range cc {
		roles[e] = core.RoleCallCenter
	}
	for _, e := range execs {
		roles[e] = core.RoleExec
	}

	cfg := core.DefaultConfig()
	cfg.Holidays = holidays

	withAttack := func(name string, build func(g *sim.Gen) (map[string]bool, time.Time, []sim.RiskUpdate)) sim.Scenario {
		g := sim.NewGen(rand.New(rand.NewSource(7)))
		attackers, t0, updates := build(g)
		return sim.Scenario{
			Name:        name,
			Calls:       append(append([]sim.Call{}, background...), g.Calls()...),
			Attackers:   attackers,
			AttackStart: t0,
			Updates:     updates,
		}
	}

	scenarios := []sim.Scenario{
		{Name: "基线(无攻击,含周末+节假日)", Calls: background, Attackers: map[string]bool{}},
		withAttack("手法1 凌晨突发30路并发打卫星高价号", func(g *sim.Gen) (map[string]bool, time.Time, []sim.RiskUpdate) {
			a, t0 := sim.Burst(g, staff[100:103], time.Date(2026, 11, 21, 2, 0, 0, 0, time.Local))
			return a, t0, nil
		}),
		withAttack("手法2 工作时间低慢速轮换12分机", func(g *sim.Gen) (map[string]bool, time.Time, []sim.RiskUpdate) {
			t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.Local)
			a, t0 := sim.LowAndSlow(g, staff[104:116], t0, t0.Add(6*time.Hour))
			return a, t0, nil
		}),
		withAttack("手法3 呼叫转移+语音信箱回拨绕过", func(g *sim.Gen) (map[string]bool, time.Time, []sim.RiskUpdate) {
			night := time.Date(2026, 11, 19, 23, 30, 0, 0, time.Local)
			noon := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local)
			a, t0 := sim.Forward(g, staff[120:125], night, noon)
			return a, t0, nil
		}),
		withAttack("手法4 利用未评级新号段(风险漂移)", func(g *sim.Gen) (map[string]bool, time.Time, []sim.RiskUpdate) {
			t0 := time.Date(2026, 11, 20, 19, 0, 0, 0, time.Local)
			a, t0 := sim.RiskDrift(g, staff[130:136], t0, t0.Add(4*time.Hour))
			updates := []sim.RiskUpdate{
				{T: t0.Add(2 * time.Hour), Prefix: "00700", Risk: 0.9, Rate: 12.0},
			}
			return a, t0, updates
		}),
	}

	fmt.Printf("模拟窗口: %s ~ %s(含周末与 1 天节假日),正常呼叫 %d 条\n\n",
		start.Format("01-02"), end.Format("01-02"), len(background))

	type row struct {
		on, off sim.Result
	}
	var rows []row
	for _, sc := range scenarios {
		on := sim.Run(sc, roles, cfg, true)
		off := sim.Run(sc, roles, cfg, false)
		rows = append(rows, row{on, off})

		fmt.Printf("=== %s ===\n", sc.Name)
		if len(sc.Attackers) == 0 {
			fmt.Printf("  正常话费: %.0f 元 | 误限制分机: %d 个 | 误拒呼叫: %d 次\n",
				on.LegitCost, len(on.FalseRestrExts), on.FalseDenied)
		} else {
			if on.Detected {
				fmt.Printf("  发现耗时: %s | 检出时损失: %.0f 元\n", on.DetectDelay.Round(time.Second), on.LossAtDetect)
			} else {
				fmt.Printf("  发现耗时: 未检出!\n")
			}
			fmt.Printf("  防护下损失: %.0f 元 | 无防护损失: %.0f 元 | 拦截欺诈呼叫: %d | 漏网完成: %d\n",
				on.FraudCost, off.FraudCost, on.FraudBlocked, on.FraudCompleted)
			fmt.Printf("  误限制分机: %d 个 | 误拒合法呼叫: %d 次\n", len(on.FalseRestrExts), on.FalseDenied)
			for i, a := range on.Actions {
				if i >= 3 {
					fmt.Printf("  ... 共 %d 次限制动作\n", len(on.Actions))
					break
				}
				fmt.Printf("    动作: %s\n", a)
			}
		}
		fmt.Println()
	}

	fmt.Println("=== 汇总 ===")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "场景\t发现耗时\t检出时损失(元)\t防护下损失(元)\t无防护损失(元)\t误限分机\t误拒呼叫")
	for _, r := range rows {
		delay := "-"
		if r.on.Detected {
			delay = r.on.DetectDelay.Round(time.Second).String()
		} else if len(rows) > 0 && r.on.Scenario != rows[0].on.Scenario {
			delay = "未检出"
		}
		fmt.Fprintf(w, "%s\t%s\t%.0f\t%.0f\t%.0f\t%d\t%d\n",
			r.on.Scenario, delay, r.on.LossAtDetect, r.on.FraudCost, r.off.FraudCost,
			len(r.on.FalseRestrExts), r.on.FalseDenied)
	}
	w.Flush()
}
