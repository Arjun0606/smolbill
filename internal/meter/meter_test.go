package meter

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Arjun0606/smolbill/internal/domain"
)

func at(day int) time.Time {
	return time.Date(2026, 6, day, 12, 0, 0, 0, time.UTC)
}

func ev(key, meterCode string, t time.Time, props map[string]any) domain.Event {
	return domain.Event{IdempotencyKey: key, MeterCode: meterCode, EventTime: t, Properties: props}
}

func TestAggregateCount(t *testing.T) {
	m := domain.Meter{Code: "api_calls", Aggregation: domain.AggCount}
	events := []domain.Event{
		ev("1", "api_calls", at(2), nil),
		ev("2", "api_calls", at(3), nil),
		ev("3", "other", at(3), nil),      // wrong meter
		ev("4", "api_calls", at(20), nil), // out of window
	}
	got, err := Aggregate(m, events, at(1), at(10))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("count = %s, want 2", got)
	}
}

func TestAggregateHalfOpenWindow(t *testing.T) {
	m := domain.Meter{Code: "c", Aggregation: domain.AggCount}
	start, end := at(5), at(10)
	events := []domain.Event{
		ev("start", "c", start, nil), // inclusive -> counted
		ev("end", "c", end, nil),     // exclusive -> not counted
	}
	got, _ := Aggregate(m, events, start, end)
	if !got.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("half-open window = %s, want 1 (start in, end out)", got)
	}
}

func TestAggregateSum(t *testing.T) {
	m := domain.Meter{Code: "tokens", Aggregation: domain.AggSum, PropertyKey: "n"}
	events := []domain.Event{
		ev("1", "tokens", at(2), map[string]any{"n": float64(100)}),
		ev("2", "tokens", at(3), map[string]any{"n": "250"}), // string numeric
		ev("3", "tokens", at(4), map[string]any{"n": int(50)}),
	}
	got, err := Aggregate(m, events, at(1), at(10))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(decimal.NewFromInt(400)) {
		t.Fatalf("sum = %s, want 400", got)
	}
}

func TestAggregateMax(t *testing.T) {
	m := domain.Meter{Code: "seats", Aggregation: domain.AggMax, PropertyKey: "active"}
	events := []domain.Event{
		ev("1", "seats", at(2), map[string]any{"active": float64(3)}),
		ev("2", "seats", at(3), map[string]any{"active": float64(9)}),
		ev("3", "seats", at(4), map[string]any{"active": float64(5)}),
	}
	got, _ := Aggregate(m, events, at(1), at(10))
	if !got.Equal(decimal.NewFromInt(9)) {
		t.Fatalf("max = %s, want 9", got)
	}
}

func TestAggregateMaxEmptyIsZero(t *testing.T) {
	m := domain.Meter{Code: "seats", Aggregation: domain.AggMax, PropertyKey: "active"}
	got, err := Aggregate(m, nil, at(1), at(10))
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Fatalf("empty max = %s, want 0", got)
	}
}

func TestAggregateUnique(t *testing.T) {
	m := domain.Meter{Code: "users", Aggregation: domain.AggUnique, PropertyKey: "uid"}
	events := []domain.Event{
		ev("1", "users", at(2), map[string]any{"uid": "alice"}),
		ev("2", "users", at(3), map[string]any{"uid": "bob"}),
		ev("3", "users", at(4), map[string]any{"uid": "alice"}), // dup value
	}
	got, _ := Aggregate(m, events, at(1), at(10))
	if !got.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("unique = %s, want 2", got)
	}
}

func TestAggregateSumMissingPropertyErrors(t *testing.T) {
	m := domain.Meter{Code: "tokens", Aggregation: domain.AggSum, PropertyKey: "n"}
	events := []domain.Event{ev("1", "tokens", at(2), map[string]any{"wrong": 1})}
	if _, err := Aggregate(m, events, at(1), at(10)); err == nil {
		t.Fatal("expected error on missing property")
	}
}
