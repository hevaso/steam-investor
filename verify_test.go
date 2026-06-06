package main

import (
	"math"
	"testing"
)

// Classic StockCharts RSI worked example. The 14-period RSI at the 15th
// price is ~70.46 in every textbook. We verify our implementation matches.
func TestRSIReference(t *testing.T) {
	p := []float64{44.34, 44.09, 44.15, 43.61, 44.33, 44.83, 45.10, 45.42,
		45.84, 46.08, 45.89, 46.03, 45.61, 46.28, 46.28}
	got := rsi(p, 14)
	if math.Abs(got-70.46) > 0.6 {
		t.Errorf("RSI off: got %.2f want ~70.46", got)
	}
	t.Logf("RSI(14) on textbook data = %.2f (expected ~70.46)", got)
}

func TestRSIEdges(t *testing.T) {
	up := []float64{}
	down := []float64{}
	for i := 0; i < 30; i++ {
		up = append(up, float64(i))
		down = append(down, float64(30-i))
	}
	if r := rsi(up, 14); r < 99 {
		t.Errorf("all-up RSI should be ~100, got %.2f", r)
	}
	if r := rsi(down, 14); r > 1 {
		t.Errorf("all-down RSI should be ~0, got %.2f", r)
	}
}

// SMA sanity: average of 1..10 over last 5 = (6+7+8+9+10)/5 = 8
func TestSMA(t *testing.T) {
	p := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := sma(p, 5); got != 8 {
		t.Errorf("SMA got %.2f want 8", got)
	}
}

// MACD on a steady uptrend should be positive (12-EMA above 26-EMA).
func TestMACDTrend(t *testing.T) {
	p := []float64{}
	for i := 0; i < 60; i++ {
		p = append(p, 10+float64(i)*0.5)
	}
	line, _, _ := macd(p)
	if line <= 0 {
		t.Errorf("MACD line on uptrend should be >0, got %.4f", line)
	}
	t.Logf("MACD line on uptrend = %.4f (expected >0)", line)
}
