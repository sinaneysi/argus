package lessons

// ratio is difflib.SequenceMatcher(None, a, b).ratio() of the reference job: 2*M/T, where M is the number of
// characters in the matching blocks (no junk; with autojunk, the "popular" characters of a long b are left out of
// the index exactly as difflib does).
func ratio(a, b []rune) float64 {
	b2j := map[rune][]int{}
	for j, c := range b {
		b2j[c] = append(b2j[c], j)
	}
	if n := len(b); n >= 200 {
		ntest := n/100 + 1
		for c, idx := range b2j {
			if len(idx) > ntest {
				delete(b2j, c)
			}
		}
	}
	matched := 0
	queue := [][4]int{{0, len(a), 0, len(b)}}
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		alo, ahi, blo, bhi := q[0], q[1], q[2], q[3]
		i, j, k := longest(a, b, b2j, alo, ahi, blo, bhi)
		if k == 0 {
			continue
		}
		matched += k
		if alo < i && blo < j {
			queue = append(queue, [4]int{alo, i, blo, j})
		}
		if i+k < ahi && j+k < bhi {
			queue = append(queue, [4]int{i + k, ahi, j + k, bhi})
		}
	}
	total := len(a) + len(b)
	if total == 0 {
		return 1.0
	}
	return 2.0 * float64(matched) / float64(total)
}

// longest is SequenceMatcher.find_longest_match without junk.
func longest(a, b []rune, b2j map[rune][]int, alo, ahi, blo, bhi int) (int, int, int) {
	besti, bestj, best := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		next := map[int]int{}
		for _, j := range b2j[a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			next[j] = k
			if k > best {
				besti, bestj, best = i-k+1, j-k+1, k
			}
		}
		j2len = next
	}
	for besti > alo && bestj > blo && a[besti-1] == b[bestj-1] {
		besti, bestj, best = besti-1, bestj-1, best+1
	}
	for besti+best < ahi && bestj+best < bhi && a[besti+best] == b[bestj+best] {
		best++
	}
	return besti, bestj, best
}
