package demo

import (
	"fmt"
	"testing"

	"github.com/sinaneysi/argus/internal/py"
)

// Expected values from CPython 3.12: random.Random(seed) then random(), randint(22, 64), uniform(8, 35),
// choice(range(5)), getrandbits(8), randint(-80, 80), randint(1, 1).
func TestPyRandMatchesCPython(t *testing.T) {
	want := map[int64]string{
		20261024:        "0.5887432484060917 26 16.322704132827518 0 49 54 1",
		20261024 * 7919: "0.4541541207863067 28 31.805145583642457 1 12 38 1",
		0:               "0.8444218515250481 48 9.093078210880993 4 124 23 1",
	}
	for seed, w := range want {
		r := newPyRand(seed)
		got := fmt.Sprintf("%s %d %s %d %d %d %d", py.FloatRepr(r.random()), r.randint(22, 64), py.FloatRepr(r.uniform(8, 35)),
			r.choice(5), r.getrandbits(8), r.randint(-80, 80), r.randint(1, 1))
		if got != w {
			t.Errorf("seed %d: got %s want %s", seed, got, w)
		}
	}
}
