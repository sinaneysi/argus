package demo

// pyRand is CPython's random.Random (Mersenne Twister MT19937) with the few methods the demo uses, so the same seed
// gives the same data as the original Python generator: random(), getrandbits(k), randint(a, b), uniform(a, b) and
// choice (as an index). The algorithms follow Modules/_randommodule.c and Lib/random.py of Python 3.12.
type pyRand struct {
	mt  [624]uint32
	mti int
}

func newPyRand(seed int64) *pyRand {
	r := &pyRand{}
	r.seed(seed)
	return r
}

func (r *pyRand) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < 624; i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i)
	}
	r.mti = 624
}

// seed is random_seed() for an int: the absolute value split into 32-bit words, least significant first.
func (r *pyRand) seed(n int64) {
	if n < 0 {
		n = -n
	}
	u := uint64(n)
	key := []uint32{}
	for u > 0 {
		key = append(key, uint32(u))
		u >>= 32
	}
	if len(key) == 0 {
		key = []uint32{0}
	}
	r.initGenrand(19650218)
	i, j := 1, 0
	k := 624
	if len(key) > k {
		k = len(key)
	}
	for ; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k = 623; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
	}
	r.mt[0] = 0x80000000
}

func (r *pyRand) uint32() uint32 {
	const upper, lower = 0x80000000, 0x7fffffff
	mag01 := [2]uint32{0, 0x9908b0df}
	if r.mti >= 624 {
		var kk int
		for kk = 0; kk < 624-397; kk++ {
			y := (r.mt[kk] & upper) | (r.mt[kk+1] & lower)
			r.mt[kk] = r.mt[kk+397] ^ (y >> 1) ^ mag01[y&1]
		}
		for ; kk < 623; kk++ {
			y := (r.mt[kk] & upper) | (r.mt[kk+1] & lower)
			r.mt[kk] = r.mt[kk+(397-624)] ^ (y >> 1) ^ mag01[y&1]
		}
		y := (r.mt[623] & upper) | (r.mt[0] & lower)
		r.mt[623] = r.mt[396] ^ (y >> 1) ^ mag01[y&1]
		r.mti = 0
	}
	y := r.mt[r.mti]
	r.mti++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

// random is random.random(): 53 random bits in [0, 1).
func (r *pyRand) random() float64 {
	a, b := r.uint32()>>5, r.uint32()>>6
	return (float64(a)*67108864.0 + float64(b)) * (1.0 / 9007199254740992.0)
}

// getrandbits is random.getrandbits(k) for 0 < k <= 32.
func (r *pyRand) getrandbits(k int) uint32 {
	return r.uint32() >> (32 - uint(k))
}

// below is Random._randbelow_with_getrandbits(n).
func (r *pyRand) below(n int) int {
	if n <= 0 {
		return 0
	}
	k := 0
	for x := n; x > 0; x >>= 1 {
		k++
	}
	v := int(r.getrandbits(k))
	for v >= n {
		v = int(r.getrandbits(k))
	}
	return v
}

// randint is random.randint(a, b), both ends included.
func (r *pyRand) randint(a, b int) int { return a + r.below(b-a+1) }

// uniform is random.uniform(a, b).
func (r *pyRand) uniform(a, b float64) float64 { return a + (b-a)*r.random() }

// choice is the index random.choice(seq) picks in a sequence of length n.
func (r *pyRand) choice(n int) int { return r.below(n) }
