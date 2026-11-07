package app

// A port of the parts of Python's difflib that views_outline uses: SequenceMatcher (isjunk=None, autojunk=True)
// over lists of lines, get_grouped_opcodes and unified_diff(..., lineterm=""). Same algorithm, so the same hunks.

import "sort"

type outlineMatch struct{ a, b, size int }

type outlineOpcode struct {
	tag            string
	i1, i2, j1, j2 int
}

type outlineSeqMatcher struct {
	a, b []string
	b2j  map[string][]int
}

func outlineNewMatcher(a, b []string) *outlineSeqMatcher {
	m := &outlineSeqMatcher{a: a, b: b, b2j: map[string][]int{}}
	for i, elt := range b {
		m.b2j[elt] = append(m.b2j[elt], i)
	}
	n := len(b)
	if n >= 200 { // autojunk: drop the popular elements (more than 1% + 1 of b)
		ntest := n/100 + 1
		var popular []string
		for elt, idx := range m.b2j {
			if len(idx) > ntest {
				popular = append(popular, elt)
			}
		}
		for _, elt := range popular {
			delete(m.b2j, elt)
		}
	}
	return m
}

func (m *outlineSeqMatcher) findLongestMatch(alo, ahi, blo, bhi int) outlineMatch {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range m.b2j[m.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	// no junk (isjunk is None): only the plain extension by equal elements applies
	for besti > alo && bestj > blo && m.a[besti-1] == m.b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && m.a[besti+bestsize] == m.b[bestj+bestsize] {
		bestsize++
	}
	return outlineMatch{besti, bestj, bestsize}
}

func (m *outlineSeqMatcher) matchingBlocks() []outlineMatch {
	la, lb := len(m.a), len(m.b)
	queue := [][4]int{{0, la, 0, lb}}
	var blocks []outlineMatch
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		alo, ahi, blo, bhi := q[0], q[1], q[2], q[3]
		x := m.findLongestMatch(alo, ahi, blo, bhi)
		if x.size > 0 {
			blocks = append(blocks, x)
			if alo < x.a && blo < x.b {
				queue = append(queue, [4]int{alo, x.a, blo, x.b})
			}
			if x.a+x.size < ahi && x.b+x.size < bhi {
				queue = append(queue, [4]int{x.a + x.size, ahi, x.b + x.size, bhi})
			}
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].a != blocks[j].a {
			return blocks[i].a < blocks[j].a
		}
		if blocks[i].b != blocks[j].b {
			return blocks[i].b < blocks[j].b
		}
		return blocks[i].size < blocks[j].size
	})
	i1, j1, k1 := 0, 0, 0
	var out []outlineMatch
	for _, x := range blocks {
		if i1+k1 == x.a && j1+k1 == x.b {
			k1 += x.size
		} else {
			if k1 > 0 {
				out = append(out, outlineMatch{i1, j1, k1})
			}
			i1, j1, k1 = x.a, x.b, x.size
		}
	}
	if k1 > 0 {
		out = append(out, outlineMatch{i1, j1, k1})
	}
	return append(out, outlineMatch{la, lb, 0})
}

func (m *outlineSeqMatcher) opcodes() []outlineOpcode {
	i, j := 0, 0
	var out []outlineOpcode
	for _, x := range m.matchingBlocks() {
		tag := ""
		if i < x.a && j < x.b {
			tag = "replace"
		} else if i < x.a {
			tag = "delete"
		} else if j < x.b {
			tag = "insert"
		}
		if tag != "" {
			out = append(out, outlineOpcode{tag, i, x.a, j, x.b})
		}
		i, j = x.a+x.size, x.b+x.size
		if x.size > 0 {
			out = append(out, outlineOpcode{"equal", x.a, i, x.b, j})
		}
	}
	return out
}

func (m *outlineSeqMatcher) groupedOpcodes(n int) [][]outlineOpcode {
	codes := m.opcodes()
	if len(codes) == 0 {
		codes = []outlineOpcode{{"equal", 0, 1, 0, 1}}
	}
	if codes[0].tag == "equal" {
		c := codes[0]
		codes[0] = outlineOpcode{c.tag, max(c.i1, c.i2-n), c.i2, max(c.j1, c.j2-n), c.j2}
	}
	if last := codes[len(codes)-1]; last.tag == "equal" {
		codes[len(codes)-1] = outlineOpcode{last.tag, last.i1, min(last.i2, last.i1+n), last.j1, min(last.j2, last.j1+n)}
	}
	nn := n + n
	var groups [][]outlineOpcode
	var group []outlineOpcode
	for _, c := range codes {
		i1, j1 := c.i1, c.j1
		if c.tag == "equal" && c.i2-c.i1 > nn {
			group = append(group, outlineOpcode{c.tag, c.i1, min(c.i2, c.i1+n), c.j1, min(c.j2, c.j1+n)})
			groups = append(groups, group)
			group = nil
			i1, j1 = max(c.i1, c.i2-n), max(c.j1, c.j2-n)
		}
		group = append(group, outlineOpcode{c.tag, i1, c.i2, j1, c.j2})
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].tag == "equal") {
		groups = append(groups, group)
	}
	return groups
}

func outlineFormatRange(start, stop int) string {
	beginning := start + 1
	length := stop - start
	if length == 1 {
		return Itoa(beginning)
	}
	if length == 0 {
		beginning--
	}
	return Itoa(beginning) + "," + Itoa(length)
}

// outlineUnifiedDiff is difflib.unified_diff(a, b, fromfile, tofile, n=n, lineterm="").
func outlineUnifiedDiff(a, b []string, fromfile, tofile string, n int) []string {
	var out []string
	started := false
	for _, group := range outlineNewMatcher(a, b).groupedOpcodes(n) {
		if !started {
			started = true
			out = append(out, "--- "+fromfile, "+++ "+tofile)
		}
		first, last := group[0], group[len(group)-1]
		out = append(out, "@@ -"+outlineFormatRange(first.i1, last.i2)+" +"+outlineFormatRange(first.j1, last.j2)+" @@")
		for _, c := range group {
			if c.tag == "equal" {
				for _, l := range a[c.i1:c.i2] {
					out = append(out, " "+l)
				}
				continue
			}
			if c.tag == "replace" || c.tag == "delete" {
				for _, l := range a[c.i1:c.i2] {
					out = append(out, "-"+l)
				}
			}
			if c.tag == "replace" || c.tag == "insert" {
				for _, l := range b[c.j1:c.j2] {
					out = append(out, "+"+l)
				}
			}
		}
	}
	return out
}
