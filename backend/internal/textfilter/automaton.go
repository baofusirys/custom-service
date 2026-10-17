package textfilter

// 多模式匹配：Aho-Corasick 自动机（按 rune），词表变更时整体重建，匹配为 O(文本长度 + 命中数)。

type acPattern struct {
	length int
	cat    Category
	ascii  bool
}

type acNode struct {
	next map[rune]int32
	fail int32
	out  []int32
}

type automaton struct {
	nodes []acNode
	pats  []acPattern
}

func buildAutomaton(words [][]rune, meta []acPattern) *automaton {
	a := &automaton{nodes: []acNode{{next: map[rune]int32{}}}, pats: meta}
	for pi, w := range words {
		cur := int32(0)
		for _, r := range w {
			nx, ok := a.nodes[cur].next[r]
			if !ok {
				a.nodes = append(a.nodes, acNode{next: map[rune]int32{}})
				nx = int32(len(a.nodes) - 1)
				a.nodes[cur].next[r] = nx
			}
			cur = nx
		}
		a.nodes[cur].out = append(a.nodes[cur].out, int32(pi))
	}
	queue := make([]int32, 0, len(a.nodes))
	for _, child := range a.nodes[0].next {
		a.nodes[child].fail = 0
		queue = append(queue, child)
	}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for r, v := range a.nodes[u].next {
			f := a.nodes[u].fail
			for f != 0 {
				if _, ok := a.nodes[f].next[r]; ok {
					break
				}
				f = a.nodes[f].fail
			}
			if nx, ok := a.nodes[f].next[r]; ok && nx != v {
				a.nodes[v].fail = nx
			} else {
				a.nodes[v].fail = 0
			}
			a.nodes[v].out = append(a.nodes[v].out, a.nodes[a.nodes[v].fail].out...)
			queue = append(queue, v)
		}
	}
	return a
}

// scan 对 text 匹配，fn 收到命中的模式与结束下标（含）。
func (a *automaton) scan(text []rune, fn func(p acPattern, end int)) {
	if a == nil || len(a.pats) == 0 {
		return
	}
	cur := int32(0)
	for i, r := range text {
		for cur != 0 {
			if _, ok := a.nodes[cur].next[r]; ok {
				break
			}
			cur = a.nodes[cur].fail
		}
		if nx, ok := a.nodes[cur].next[r]; ok {
			cur = nx
		}
		for _, pi := range a.nodes[cur].out {
			fn(a.pats[pi], i)
		}
	}
}
