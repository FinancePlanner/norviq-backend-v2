package nsdecimal

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// testdata/vectors.txt was produced by a Swift 6.2.3 program (macOS 26,
// swift-foundation release/6.2) printing Decimal results:
//
//	F <float64 bits> <Decimal(Double).description>
//	O a b a+b a-b a*b a/b cmp(a,b) round(a,2,.plain) round(a,0,.down) doubleValue-bits (intValue unused)
func TestSwiftVectors(t *testing.T) {
	f, err := os.Open("testdata/vectors.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	fails := 0
	check := func(what, got, want string) {
		if got != want {
			fails++
			if fails < 40 {
				t.Errorf("line %d %s: got %s want %s", line, what, got, want)
			}
		}
	}
	for sc.Scan() {
		line++
		p := strings.Fields(sc.Text())
		switch p[0] {
		case "F":
			bits, _ := strconv.ParseUint(p[1], 10, 64)
			check("FromFloat64", FromFloat64(math.Float64frombits(bits)).String(), p[2])
		case "O":
			a, b := MustParse(p[1]), MustParse(p[2])
			check("parse a", a.String(), p[1])
			check("add", a.Add(b).String(), p[3])
			check("sub", a.Sub(b).String(), p[4])
			check("mul", a.Mul(b).String(), p[5])
			check("div", a.Div(b).String(), p[6])
			check("cmp", strconv.Itoa(a.Cmp(b)), p[7])
			check("round2", a.Round(2, RoundPlain).String(), p[8])
			check("round0down", a.Round(0, RoundDown).String(), p[9])
			check("double", strconv.FormatUint(math.Float64bits(a.Float64()), 10), p[10])
		}
	}
	if fails > 0 {
		t.Fatalf("%d mismatches", fails)
	}
}

func TestKnownValues(t *testing.T) {
	cases := map[string]string{
		FromInt(1).Div(FromInt(3)).String():                                    "0.33333333333333333333333333333333333333",
		FromInt(2).Div(FromInt(3)).String():                                    "0.66666666666666666666666666666666666666",
		MustParse("1E30").Div(FromInt(7)).String():                             "142857142857142857142857142857.142857142",
		FromInt(1).Div(FromInt(3)).Add(FromInt(1000)).String():                 "1000.333333333333333333333333333333",
		FromFloat64(3.133).String():                                            "3.132999999999999488",
		FromFloat64(0.1).String():                                              "0.1",
		FromFloat64(1234567.891).String():                                      "1234567.8910000001024",
		MustParse("-0.50").String():                                            "-0.5",
		strconv.FormatFloat(FromInt(1).Div(FromInt(3)).Float64(), 'g', -1, 64): "0.33333333333333337",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %s want %s", got, want)
		}
	}
}
