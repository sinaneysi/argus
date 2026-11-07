package py

// Reference values produced by CPython 3.12 (repr, format specs, parse_qs, quote, html.escape, splitlines,
// SimpleCookie, json.dumps): the Go helpers must give the same bytes.

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
)

func hexf(t *testing.T, s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatal(s, err)
	}
	return f
}

func TestFloatFormats(t *testing.T) {
	var repr [][2]string
	json.Unmarshal([]byte(`[["0x0.0p+0", "0.0"], ["0x1.0000000000000p+0", "1.0"], ["-0x1.8000000000000p+0", "-1.5"], ["0x1.8000000000000p+1", "3.0"], ["0x1.1c37937e08000p+53", "1e+16"], ["0x1.f75104d551d69p-17", "1.5e-05"], ["0x1.a36e2eb1c432dp-14", "0.0001"], ["0x1.d6f34547df3b6p+26", "123456789.123"], ["0x1.0f0cf064dd592p+73", "1e+22"], ["0x1.4000000000000p+1", "2.5"], ["0x1.3333333333334p-2", "0.30000000000000004"], ["0x1.5555555555555p-2", "0.3333333333333333"], ["-0x0.0p+0", "-0.0"], ["0x1.5ee2a2eb5a5c4p+53", "1.2345678901234568e+16"], ["0x0.0000000000001p-1022", "5e-324"], ["0x1.fffffffffffffp+1023", "1.7976931348623157e+308"], ["0x1.9000000000000p+6", "100.0"], ["0x1.c6bf526340000p+49", "1000000000000000.0"], ["0x1.c6bf52633ffffp+49", "999999999999999.9"]]`), &repr)
	for _, r := range repr {
		if got := FloatRepr(hexf(t, r[0])); got != r[1] {
			t.Errorf("repr %s: %s != %s", r[0], got, r[1])
		}
	}
	var g4 [][2]string
	json.Unmarshal([]byte(`[["0x0.0p+0", "0"], ["0x1.8000000000000p+0", "1.5"], ["0x1.8000000000000p+3", "12"], ["0x1.86a0000000000p+16", "1e+05"], ["0x1.e240000000000p+16", "1.235e+05"], ["0x1.4f8b588e368f1p-17", "1e-05"], ["0x1.02e4b6ce5dc68p-13", "0.0001234"], ["0x1.869f800000000p+16", "1e+05"], ["0x1.34a456d5cfaadp+10", "1235"], ["-0x1.0c6f7a0b5ed8dp-22", "-2.5e-07"], ["0x1.249ad2594c37dp+332", "1e+100"], ["0x1.0000000000000p-1", "0.5"], ["0x1.f19999999999ap+3", "15.55"], ["0x1.e847e00000000p+19", "1e+06"]]`), &g4)
	for _, r := range g4 {
		if got := G(hexf(t, r[0]), 4); got != r[1] {
			t.Errorf("g4 %s: %s != %s", r[0], got, r[1])
		}
	}
	var fs [][5]string
	json.Unmarshal([]byte(`[["0x1.0000000000000p-1", "0", "0.5", "0.50", "5.000e-01"], ["0x1.8000000000000p+0", "2", "1.5", "1.50", "1.500e+00"], ["0x1.4000000000000p+1", "2", "2.5", "2.50", "2.500e+00"], ["0x1.0000000000000p-3", "0", "0.1", "0.12", "1.250e-01"], ["0x1.0147ae147ae14p+0", "1", "1.0", "1.00", "1.005e+00"], ["-0x1.0000000000000p-1", "-0", "-0.5", "-0.50", "-5.000e-01"], ["0x1.34a456d5cfaadp+10", "1235", "1234.6", "1234.57", "1.235e+03"], ["0x1.dcd6500000000p+29", "1000000000", "1000000000.0", "1000000000.00", "1.000e+09"]]`), &fs)
	for _, r := range fs {
		x := hexf(t, r[0])
		if F(x, 0) != r[1] || F(x, 1) != r[2] || F(x, 2) != r[3] || Exp(x, 3) != r[4] {
			t.Errorf("formats %s: %s %s %s %s", r[0], F(x, 0), F(x, 1), F(x, 2), Exp(x, 3))
		}
	}
}

func TestParseQSQuote(t *testing.T) {
	cases := map[string][][2]any{
		"a=1&b=&c&a=2":             {{"a", []string{"1", "2"}}},
		"x=%E2%9C%93+y&z=%ZZ&q=%2": {{"x", []string{"\u2713 y"}}, {"z", []string{"%ZZ"}}, {"q", []string{"%2"}}},
		"next=%2Fcerts&next=/x":    {{"next", []string{"/certs", "/x"}}},
		";a=1":                     {{";a", []string{"1"}}},
		"=v&k=":                    {{"", []string{"v"}}},
		"a=b=c":                    {{"a", []string{"b=c"}}},
	}
	for s, want := range cases {
		q := ParseQS(s)
		if len(q.Keys()) != len(want) {
			t.Fatalf("%q: keys %v", s, q.Keys())
		}
		for i, w := range want {
			if q.Keys()[i] != w[0] || !reflect.DeepEqual(q.List(w[0].(string)), w[1]) {
				t.Errorf("%q: %v", s, q.Keys())
			}
		}
	}
	for _, c := range [][4]string{{"a b/c", "a%20b/c", "a+b%2Fc", "k=a+b%2Fc"},
		{"\u00e9~_.-*'()", "%C3%A9~_.-%2A%27%28%29", "%C3%A9~_.-%2A%27%28%29", "k=%C3%A9~_.-%2A%27%28%29"},
		{"2026-10-20 x.md", "2026-10-20%20x.md", "2026-10-20+x.md", "k=2026-10-20+x.md"},
		{"x&y=z#", "x%26y%3Dz%23", "x%26y%3Dz%23", "k=x%26y%3Dz%23"}} {
		if Quote(c[0]) != c[1] || QuotePlus(c[0]) != c[2] || UrlencodeKV("k", c[0]) != c[3] {
			t.Errorf("quote %q: %s %s %s", c[0], Quote(c[0]), QuotePlus(c[0]), UrlencodeKV("k", c[0]))
		}
	}
	if E(`<a href='x'>&"`) != "&lt;a href=&#x27;x&#x27;&gt;&amp;&quot;" {
		t.Fatal("escape")
	}
}

func TestSplitLinesSlice(t *testing.T) {
	if got := SplitLines("a\nb\r\nc\rd\x0be\x0cf\x1cg\u0085h\u2028i\n"); !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}) {
		t.Fatal(got)
	}
	if got := SplitLines("\n\nx"); !reflect.DeepEqual(got, []string{"", "", "x"}) {
		t.Fatal(got)
	}
	if len(SplitLines("")) != 0 || !reflect.DeepEqual(SplitLines("x\n"), []string{"x"}) {
		t.Fatal("edge")
	}
	if Slice("héllo", 0, -3) != "hé" || Head("héllo", 2) != "hé" || Slice("abc", 5) != "" || Len("héllo") != 5 {
		t.Fatal("slice")
	}
}

func TestSimpleCookie(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
		err  bool
	}{
		{"opsr_session=abc; opsr_theme=dark", map[string]string{"opsr_session": "abc", "opsr_theme": "dark"}, false},
		{"a=1; a=2", map[string]string{"a": "2"}, false},
		{`opsr_theme="dark"><script>; x`, map[string]string{}, false},
		{"foo/bar=1; opsr_session=x", nil, true},
		{"Path=/; a=1", map[string]string{}, false},
		{"a=1; Path=/; b=2", map[string]string{"a": "1", "b": "2"}, false},
		{`a="q\"t"; b=\012`, map[string]string{"a": `q"t`}, false},
		{"$Version=1; a=b", map[string]string{"a": "b"}, false},
		{"x; a=1", map[string]string{}, false},
		{"a=1;b=2,c=3", map[string]string{"a": "1", "b": "2,c=3"}, false},
	}
	for _, c := range cases {
		got, err := ParseCookies(c.in)
		if (err != nil) != c.err || (!c.err && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%q: %v %v", c.in, got, err)
		}
	}
}

func TestDumpsAndOrderedJSON(t *testing.T) {
	v, err := ParseJSON([]byte(`{"b":[1,2.5,{"x":null,"é":"é\u2028😀","t":true}],"a":"q\"\\/<>&\u007f"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n \"b\": [\n  1,\n  2.5,\n  {\n   \"x\": null,\n   \"\\u00e9\": \"\\u00e9\\u2028\\ud83d\\ude00\",\n   \"t\": true\n  }\n ],\n \"a\": \"q\\\"\\\\/<>&\\u007f\"\n}"
	if got := Dumps(v, 1); got != want {
		t.Fatalf("%q", got)
	}
	if Dumps(NewOMap("by", "u", "at", int64(5)), -1) != `{"by": "u", "at": 5}` || Dumps([]any{}, 1) != "[]" ||
		Dumps(NewOMap(), 1) != "{}" || Dumps(NewOMap("a", []any{}), 1) != "{\n \"a\": []\n}" {
		t.Fatal("dumps small")
	}
	m := v.(*OMap)
	if !reflect.DeepEqual(m.Keys(), []string{"b", "a"}) || !IsInt(m.List("b")[0]) || IsInt(m.List("b")[1]) {
		t.Fatal("order / int")
	}
}

func TestIntParsing(t *testing.T) {
	for s, want := range map[string]int64{"12": 12, " 7 ": 7, "-3": -3, "1_000": 1000, "\u06f1\u06f2": 12} {
		if n, ok := ParseInt(s); !ok || n != want {
			t.Errorf("%q: %d %v", s, n, ok)
		}
	}
	for _, s := range []string{"", "1.5", "_1", "1__0", "0x10", "abc"} {
		if _, ok := ParseInt(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
	if !IsDigit("123") || IsDigit("") || IsDigit("-1") || IsDigit("1.0") {
		t.Fatal("isdigit")
	}
}

func TestFastJSONMatchesStd(t *testing.T) {
	ins := []string{`{"a":1,"b":[1,2.5,-0,1e3,1E-2,123456789012345678901234567890],"c":{"x":null,"y":true,"z":"q\"é😀\n"},"a":2}`,
		`[]`, `{}`, ` [ "x" , {"k" : [ ] } ] `, `"\ud800"`, `-1.5`, `0`}
	for _, in := range ins {
		a, e1 := parseJSONFast([]byte(in))
		b, e2 := parseJSONStd([]byte(in))
		if (e1 != nil) != (e2 != nil) || Dumps(a, 1) != Dumps(b, 1) {
			t.Errorf("%s: %v %v\n%s\n%s", in, e1, e2, Dumps(a, 1), Dumps(b, 1))
		}
	}
	for _, bad := range []string{`{`, `[1,]`, `{"a" 1}`, `01`, `1.`, `"a` + "\x01" + `"`, `[1] x`, `{"a":1,}`, `tru`, ``} {
		if _, err := parseJSONFast([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if v, err := ParseJSON([]byte(`[NaN, Infinity, -Infinity]`)); err != nil || len(v.([]any)) != 3 {
		t.Fatal("python literals")
	}
}
