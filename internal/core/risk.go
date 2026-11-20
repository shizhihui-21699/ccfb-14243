package core

// PrefixRisk 描述一个号码段的风险与费率。
// Risk 取 0..1,综合了"是否国际/声讯/卫星电话"和"该号段近期是否被用于盗打"。
type PrefixRisk struct {
	Prefix     string
	Risk       float64
	RatePerMin float64 // 话费单价(元/分钟)
}

// RiskTable 是号码段风险表,最长前缀匹配。
// 运营商/安全团队会动态更新它(Set),新出现的高价号段先给低分、确认后调高。
type RiskTable struct {
	entries []PrefixRisk
}

func NewRiskTable() *RiskTable {
	t := &RiskTable{}
	// 卫星电话:盗打重灾区
	t.Set("00882", 0.95, 15.0) // Inmarsat 等
	t.Set("00881", 0.95, 12.0)
	// 高风险国家/地区
	t.Set("00252", 0.85, 6.0) // 索马里
	// 中等风险国际方向
	t.Set("00853", 0.50, 4.0) // 澳门
	t.Set("00701", 0.40, 3.0)
	// 新出现的号段,运营商尚未评级,暂给低风险 —— 风险漂移场景会动态调高
	t.Set("00700", 0.15, 3.0)
	// 常见商务方向
	t.Set("00852", 0.20, 1.0) // 香港
	t.Set("001", 0.10, 0.6)   // 美国/加拿大
	t.Set("0044", 0.12, 0.8)  // 英国
	t.Set("0049", 0.12, 0.8)  // 德国
	// 兜底:其他国际 / 国内长途 / 手机
	t.Set("00", 0.30, 1.5)
	t.Set("0", 0.02, 0.2)
	t.Set("1", 0.02, 0.15)
	return t
}

// Set 动态更新(或新增)一个号段的风险评级。
func (t *RiskTable) Set(prefix string, risk, rate float64) {
	for i := range t.entries {
		if t.entries[i].Prefix == prefix {
			t.entries[i].Risk = risk
			t.entries[i].RatePerMin = rate
			return
		}
	}
	t.entries = append(t.entries, PrefixRisk{Prefix: prefix, Risk: risk, RatePerMin: rate})
}

// Lookup 最长前缀匹配,返回 (风险, 费率)。无匹配时给一个很低的默认值。
func (t *RiskTable) Lookup(number string) (risk, rate float64) {
	best := -1
	for i, e := range t.entries {
		if len(e.Prefix) <= len(number) && number[:len(e.Prefix)] == e.Prefix {
			if best < 0 || len(e.Prefix) > len(t.entries[best].Prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return 0.05, 0.1
	}
	return t.entries[best].Risk, t.entries[best].RatePerMin
}
