package alerts

import (
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

func pct(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

func TestCrossed(t *testing.T) {
	th := []int{50, 80, 100}
	cases := []struct {
		name     string
		maxFired int
		pct      string
		want     []int
		wantMax  int
	}{
		{"below first", 0, "30", nil, 0},
		{"hits 50", 0, "55", []int{50}, 50},
		{"jumps past 50 and 80 at once", 0, "85", []int{50, 80}, 80},
		{"already fired 50, now 80", 50, "82", []int{80}, 80},
		{"already fired 80, still 82", 80, "82", nil, 80},
		{"hits 100 exactly", 80, "100", []int{100}, 100},
		{"over 100", 100, "140", nil, 100},
		{"all at once from cold", 0, "100", []int{50, 80, 100}, 100},
	}
	for _, c := range cases {
		got, gotMax := Crossed(c.maxFired, pct(c.pct), th)
		if !reflect.DeepEqual(got, c.want) || gotMax != c.wantMax {
			t.Errorf("%s: Crossed(%d, %s) = %v/%d, want %v/%d", c.name, c.maxFired, c.pct, got, gotMax, c.want, c.wantMax)
		}
	}
}

func TestCrossedUnsortedThresholds(t *testing.T) {
	got, max := Crossed(0, pct("85"), []int{100, 50, 80})
	if !reflect.DeepEqual(got, []int{50, 80}) || max != 80 {
		t.Fatalf("unsorted thresholds = %v/%d, want [50 80]/80", got, max)
	}
}
