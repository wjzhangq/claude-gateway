package db

import (
	"fmt"
	"sort"
)

// --- Token 归口 (Attribution) ---
//
// Derives the ASDC&SWS&NBC / SMB token attribution from DB columns populated by
// cmd/orgimport (attr_side / attr_group / is_departed) merged with live token/cost
// from daily_stats + aws_daily_stats. Mirrors the shape of the old sky-insight
// /api/attribution response so the frontend port is 1:1.

// AttrMember is one person inside an attribution group.
type AttrMember struct {
	Itcode   string  `json:"itcode"`
	Name     string  `json:"name"`
	Mgr1     string  `json:"mgr1_name"`
	Mgr2     string  `json:"mgr2"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
}

// AttrGroup aggregates one leader's group (org_count = snapshot size,
// active_count = members with tokens > 0).
type AttrGroup struct {
	Side        string        `json:"side"`
	Leader      string        `json:"leader"`
	Tokens      int64         `json:"tokens"`
	Cost        float64       `json:"cost"`
	Requests    int64         `json:"requests"`
	OrgCount    int           `json:"org_count"`
	ActiveCount int           `json:"active_count"`
	Members     []*AttrMember `json:"members"`
}

// AttrSide is one business-line team (shen | non) with its groups.
type AttrSide struct {
	Label       string       `json:"label"`
	Groups      []*AttrGroup `json:"groups"`
	GroupCount  int          `json:"group_count"`
	OrgCount    int          `json:"org_count"`
	ActiveCount int          `json:"active_count"`
	Tokens      int64        `json:"tokens"`
	Cost        float64      `json:"cost"`
	Requests    int64        `json:"requests"`
}

// AttrDiag is a diagnostic row (unmatched / departed): consumed but not attributed.
type AttrDiag struct {
	Itcode string  `json:"itcode"`
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// Attribution is the full response.
type Attribution struct {
	Days           int         `json:"days"`
	Period         string      `json:"period"`
	Shen           *AttrSide   `json:"shen"`
	Non            *AttrSide   `json:"non"`
	Unmatched      []*AttrDiag `json:"unmatched"`
	UnmatchedTotal int         `json:"unmatched_total"`
	Departed       []*AttrDiag `json:"departed"`
	DepartedTotal  int         `json:"departed_total"`
	DepartedTokens int64       `json:"departed_tokens"`
	DepartedCost   float64     `json:"departed_cost"`
}

// attrRow is one user row from the merged query.
type attrRow struct {
	itcode     string
	name       string
	mgr1       string
	mgr2       string
	side       string
	group      string
	isDeparted bool
	tokens     int64
	cost       float64
	requests   int64
}

// GetAttribution returns the token attribution grouped by side → leader group.
// days=0 means all-time; otherwise the last N days (with +8h offset to match CST日界).
// shenLabel / nonLabel are the human display names for the two sides.
func (d *DB) GetAttribution(days int, hidden []string, shenLabel, nonLabel string) (*Attribution, error) {
	dateFilter := ""
	if days > 0 {
		dateFilter = fmt.Sprintf("WHERE date >= date('now', '+8 hours', '-%d days')", days)
	}
	hiddenClause, hiddenArgs := hiddenItcodeClause("u.itcode", hidden)
	sql := fmt.Sprintf(`
	SELECT u.itcode, COALESCE(u.name, ''),
	  COALESCE(u.mgr1_name, ''), COALESCE(u.mgr2_name, ''),
	  COALESCE(u.attr_side, ''), COALESCE(u.attr_group, ''), u.is_departed,
	  COALESCE(b.total_tokens, 0) + COALESCE(a.total_tokens, 0) as tokens,
	  ROUND(COALESCE(b.cost, 0) + COALESCE(a.cost, 0), 4) as cost,
	  COALESCE(b.requests, 0) + COALESCE(a.requests, 0) as requests
	FROM users u
	LEFT JOIN (
	  SELECT user_id, SUM(total_tokens) as total_tokens, SUM(cost_usd) as cost, SUM(requests) as requests
	  FROM daily_stats %s GROUP BY user_id
	) b ON u.id = b.user_id
	LEFT JOIN (
	  SELECT user_id, SUM(total_tokens) as total_tokens, SUM(cost_usd) as cost, SUM(requests) as requests
	  FROM aws_daily_stats %s GROUP BY user_id
	) a ON u.id = a.user_id
	WHERE u.itcode IS NOT NULL AND u.itcode != ''%s`, dateFilter, dateFilter, hiddenClause)

	rows, err := d.Query(sql, hiddenArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []attrRow
	for rows.Next() {
		var r attrRow
		if err := rows.Scan(&r.itcode, &r.name, &r.mgr1, &r.mgr2,
			&r.side, &r.group, &r.isDeparted, &r.tokens, &r.cost, &r.requests); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return foldAttribution(all, days, shenLabel, nonLabel), nil
}

// foldAttribution collapses per-user rows into the side → group → member tree,
// splitting off departed and unmatched diagnostics. Pure function for testability.
func foldAttribution(all []attrRow, days int, shenLabel, nonLabel string) *Attribution {
	// group key = side|leader → *AttrGroup
	groups := map[string]*AttrGroup{}
	var unmatched, departed []*AttrDiag

	for _, r := range all {
		// Departed: excluded from team rollups, listed separately (only if they consumed).
		if r.isDeparted {
			if r.tokens > 0 {
				departed = append(departed, &AttrDiag{Itcode: r.itcode, Tokens: r.tokens, Cost: r.cost})
			}
			continue
		}
		// Unattributed but consumed → coverage diagnostic.
		if r.side == "" || r.group == "" {
			if r.tokens > 0 {
				unmatched = append(unmatched, &AttrDiag{Itcode: r.itcode, Tokens: r.tokens, Cost: r.cost})
			}
			continue
		}
		if r.side != "shen" && r.side != "non" {
			continue // unknown side value, skip defensively
		}
		key := r.side + "|" + r.group
		g := groups[key]
		if g == nil {
			g = &AttrGroup{Side: r.side, Leader: r.group, Members: []*AttrMember{}}
			groups[key] = g
		}
		g.OrgCount++
		if r.tokens > 0 {
			g.ActiveCount++
		}
		g.Tokens += r.tokens
		g.Cost += r.cost
		g.Requests += r.requests
		g.Members = append(g.Members, &AttrMember{
			Itcode:   r.itcode,
			Name:     r.name,
			Mgr1:     r.mgr1,
			Mgr2:     r.mgr2,
			Tokens:   r.tokens,
			Cost:     round4(r.cost),
			Requests: r.requests,
		})
	}

	shen := newSide(shenLabel)
	non := newSide(nonLabel)
	for _, g := range groups {
		g.Cost = round4(g.Cost)
		sort.SliceStable(g.Members, func(i, j int) bool { return g.Members[i].Tokens > g.Members[j].Tokens })
		if g.Side == "shen" {
			shen.Groups = append(shen.Groups, g)
		} else {
			non.Groups = append(non.Groups, g)
		}
	}
	finalizeSide(shen)
	finalizeSide(non)

	sortDiag(unmatched)
	sortDiag(departed)

	var departedTokens int64
	var departedCost float64
	for _, dd := range departed {
		departedTokens += dd.Tokens
		departedCost += dd.Cost
	}

	period := "全量"
	if days > 0 {
		period = fmt.Sprintf("近%d天", days)
	}

	return &Attribution{
		Days:           days,
		Period:         period,
		Shen:           shen,
		Non:            non,
		Unmatched:      capDiag(unmatched, 50),
		UnmatchedTotal: len(unmatched),
		Departed:       capDiag(departed, 50),
		DepartedTotal:  len(departed),
		DepartedTokens: departedTokens,
		DepartedCost:   round4(departedCost),
	}
}

func newSide(label string) *AttrSide {
	return &AttrSide{Label: label, Groups: []*AttrGroup{}}
}

// finalizeSide sorts groups by tokens desc and rolls up side-level totals.
func finalizeSide(s *AttrSide) {
	sort.SliceStable(s.Groups, func(i, j int) bool { return s.Groups[i].Tokens > s.Groups[j].Tokens })
	s.GroupCount = len(s.Groups)
	for _, g := range s.Groups {
		s.OrgCount += g.OrgCount
		s.ActiveCount += g.ActiveCount
		s.Tokens += g.Tokens
		s.Cost += g.Cost
		s.Requests += g.Requests
	}
	s.Cost = round4(s.Cost)
}

func sortDiag(d []*AttrDiag) {
	sort.SliceStable(d, func(i, j int) bool { return d[i].Tokens > d[j].Tokens })
}

func capDiag(d []*AttrDiag, n int) []*AttrDiag {
	if d == nil {
		return []*AttrDiag{}
	}
	if len(d) > n {
		return d[:n]
	}
	return d
}

func round4(f float64) float64 {
	return float64(int64(f*10000+0.5)) / 10000
}
