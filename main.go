package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
//  DATA TYPES
// ─────────────────────────────────────────────────────────────────────────────

// ChartPoint is one point on the price line.
// Real (historical) points have IsPredict=false.
// Forecast points have IsPredict=true and carry an Upper/Lower uncertainty band.
type ChartPoint struct {
	Price     float64  `json:"price"`
	IsPredict bool     `json:"is_predict"`
	Upper     *float64 `json:"upper,omitempty"` // top of the confidence cone (forecast only)
	Lower     *float64 `json:"lower,omitempty"` // bottom of the confidence cone (forecast only)
}

// TickResponse is the live snapshot sent to the front-end every poll.
// It carries the current price, the aggregate signal, AND the raw value of
// every indicator so the UI can teach the user what is going on.
type TickResponse struct {
	Price    float64           `json:"price"`
	Signal   string            `json:"signal"`      // BUY / SELL / HOLD (aggregate of all indicators)
	Rsi      float64           `json:"rsi"`         // Relative Strength Index (0-100)
	Sma      float64           `json:"sma"`         // Simple Moving Average (20)
	Ema      float64           `json:"ema"`         // Exponential Moving Average (20)
	MacdLine float64           `json:"macd"`        // MACD line (EMA12 - EMA26)
	MacdSig  float64           `json:"macd_signal"` // signal line (EMA9 of MACD)
	MacdHist float64           `json:"macd_hist"`   // histogram (MACD - signal)
	BollUp   float64           `json:"boll_upper"`  // upper Bollinger band
	BollMid  float64           `json:"boll_mid"`    // middle Bollinger band (= SMA20)
	BollLow  float64           `json:"boll_lower"`  // lower Bollinger band
	Votes    map[string]string `json:"votes"`       // what each indicator "thinks": BUY/SELL/HOLD
	Note     string            `json:"note"`        // honesty reminder shown in the UI
}

// ─────────────────────────────────────────────────────────────────────────────
//  GLOBAL STATE
// ─────────────────────────────────────────────────────────────────────────────

var (
	history    []ChartPoint // REAL points only (the past)
	prediction []ChartPoint // forecast segment, rebuilt every tick
	snapshot   TickResponse // latest indicator snapshot
	mu         sync.Mutex

	currentPrice float64 = 3.00 // live "real" price the simulator walks around
	basePrice    float64 = 3.00 // the value the simulated price gently reverts to
)

const (
	maxHistory   = 90 // how many real points we keep
	forecastLen  = 8  // how many future points we project
	tickInterval = 800 * time.Millisecond
)

// ─────────────────────────────────────────────────────────────────────────────
//  MAIN
// ─────────────────────────────────────────────────────────────────────────────

func setupCORS(w *http.ResponseWriter, r *http.Request) bool {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	(*w).Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == "OPTIONS" {
		(*w).WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func main() {
	rand.Seed(time.Now().UnixNano())

	fmt.Println("[SYSTEM] Генериране на начална история за Sealed Dead Hand Terminal...")
	generateInitialHistory(60) // seed enough points so every indicator is "warm" immediately
	recompute()                // build the first snapshot + forecast

	go runMarketEngine()

	http.HandleFunc("/api/market-data", handleMarketData)
	http.HandleFunc("/api/tick", handleTick)

	fmt.Println("[SYSTEM] Сървърът е пуснат успешно на http://127.0.0.1:8080")
	fmt.Println("[SYSTEM] Отвори index.html в браузъра...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ─────────────────────────────────────────────────────────────────────────────
//  THE SIMULATED MARKET
//  NOTE: this is a *simulator*. The price is a mean-reverting random walk, NOT
//  real Steam Market data. See the README notes for how to plug in real prices.
// ─────────────────────────────────────────────────────────────────────────────

func runMarketEngine() {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		<-ticker.C
		mu.Lock()
		stepPrice()
		recompute()
		mu.Unlock()
	}
}

// stepPrice moves the simulated price one tick: a little pull toward the base
// price (so it oscillates realistically instead of wandering off) plus noise.
func stepPrice() {
	reversion := (basePrice - currentPrice) * 0.02
	noise := (rand.Float64() * 0.12) - 0.06
	currentPrice += reversion + noise
	if currentPrice < 0.20 {
		currentPrice = 0.20
	}
	history = append(history, ChartPoint{Price: round2(currentPrice), IsPredict: false})
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}
}

// generateInitialHistory pre-fills the chart so it isn't empty on first load.
func generateInitialHistory(points int) {
	mu.Lock()
	defer mu.Unlock()
	for i := 0; i < points; i++ {
		reversion := (basePrice - currentPrice) * 0.02
		noise := (rand.Float64() * 0.10) - 0.05
		currentPrice += reversion + noise
		if currentPrice < 0.20 {
			currentPrice = 0.20
		}
		history = append(history, ChartPoint{Price: round2(currentPrice), IsPredict: false})
	}
}

// recompute recalculates every indicator, the aggregate signal, and the
// forecast segment. Called after each price step (already under mu.Lock()).
func recompute() {
	prices := realPrices()

	rsiV := rsi(prices, 14)
	smaV := sma(prices, 20)
	emaV := lastF(emaSeries(prices, 20))
	macdLine, macdSig, macdHist := macd(prices)
	bMid, bUp, bLow := bollinger(prices, 20, 2.0)

	votes, signal := decideSignal(round2(currentPrice), rsiV, macdHist, bLow, bUp, emaV)

	snapshot = TickResponse{
		Price:    round2(currentPrice),
		Signal:   signal,
		Rsi:      round2(rsiV),
		Sma:      round2(smaV),
		Ema:      round2(emaV),
		MacdLine: round4(macdLine),
		MacdSig:  round4(macdSig),
		MacdHist: round4(macdHist),
		BollUp:   round2(bUp),
		BollMid:  round2(bMid),
		BollLow:  round2(bLow),
		Votes:    votes,
		Note:     "Симулирана среда. Индикаторите ОПИСВАТ миналото; те не предсказват бъдещето със сигурност.",
	}

	prediction = buildForecast(prices, forecastLen)
}

// ─────────────────────────────────────────────────────────────────────────────
//  TECHNICAL INDICATORS  (the actual math)
// ─────────────────────────────────────────────────────────────────────────────

// SMA — Simple Moving Average. The plain average of the last `period` prices.
// It smooths out noise so you can see the underlying trend.
func sma(prices []float64, period int) float64 {
	if period <= 0 || len(prices) < period {
		return lastF(prices)
	}
	sum := 0.0
	for i := len(prices) - period; i < len(prices); i++ {
		sum += prices[i]
	}
	return sum / float64(period)
}

// EMA — Exponential Moving Average. Like SMA but weights recent prices more
// heavily, so it reacts faster to new moves. Returns the full series because
// MACD needs an EMA-of-an-EMA.
func emaSeries(prices []float64, period int) []float64 {
	n := len(prices)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	k := 2.0 / (float64(period) + 1.0)
	if n < period {
		out[0] = prices[0]
		for i := 1; i < n; i++ {
			out[i] = (prices[i]-out[i-1])*k + out[i-1]
		}
		return out
	}
	// seed with the SMA of the first `period` values
	seed := 0.0
	for i := 0; i < period; i++ {
		seed += prices[i]
	}
	seed /= float64(period)
	for i := 0; i < period; i++ {
		out[i] = seed
	}
	for i := period; i < n; i++ {
		out[i] = (prices[i]-out[i-1])*k + out[i-1]
	}
	return out
}

// RSI — Relative Strength Index (0-100), using Wilder's smoothing.
// >70 is "overbought" (price may have risen too fast); <30 is "oversold".
// It is a momentum gauge, NOT a price predictor.
func rsi(prices []float64, period int) float64 {
	if len(prices) <= period {
		return 50.0 // neutral until we have enough data
	}
	var gain, loss float64
	for i := 1; i <= period; i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			gain += change
		} else {
			loss -= change
		}
	}
	avgGain := gain / float64(period)
	avgLoss := loss / float64(period)
	for i := period + 1; i < len(prices); i++ {
		change := prices[i] - prices[i-1]
		var g, l float64
		if change > 0 {
			g = change
		} else {
			l = -change
		}
		avgGain = (avgGain*float64(period-1) + g) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + l) / float64(period)
	}
	if avgLoss == 0 {
		return 100.0
	}
	rs := avgGain / avgLoss
	return 100.0 - (100.0 / (1.0 + rs))
}

// MACD — Moving Average Convergence Divergence.
// macdLine = EMA12 - EMA26 (momentum), signal = EMA9 of macdLine.
// histogram = macdLine - signal. Histogram crossing above 0 is bullish.
func macd(prices []float64) (macdLine, signalLine, hist float64) {
	if len(prices) < 2 {
		return 0, 0, 0
	}
	ema12 := emaSeries(prices, 12)
	ema26 := emaSeries(prices, 26)
	n := len(prices)
	series := make([]float64, n)
	for i := 0; i < n; i++ {
		series[i] = ema12[i] - ema26[i]
	}
	sig := emaSeries(series, 9)
	macdLine = series[n-1]
	signalLine = sig[n-1]
	hist = macdLine - signalLine
	return
}

// Bollinger Bands — a moving average (mid) with bands `k` standard deviations
// above and below. Price touching the upper band = stretched high; lower = low.
func bollinger(prices []float64, period int, k float64) (mid, upper, lower float64) {
	mid = sma(prices, period)
	sd := stddev(prices, period)
	return mid, mid + k*sd, mid - k*sd
}

func stddev(prices []float64, period int) float64 {
	if period <= 0 || len(prices) < period {
		period = len(prices)
	}
	if period == 0 {
		return 0
	}
	m := sma(prices, period)
	sum := 0.0
	for i := len(prices) - period; i < len(prices); i++ {
		d := prices[i] - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(period))
}

// ─────────────────────────────────────────────────────────────────────────────
//  SIGNAL AGGREGATION
//  Each indicator casts a vote. We sum them. This deliberately shows that
//  indicators OFTEN DISAGREE — that disagreement is the honest reality.
// ─────────────────────────────────────────────────────────────────────────────

func decideSignal(price, rsiV, macdHist, bollLow, bollUp, emaV float64) (map[string]string, string) {
	votes := map[string]string{}
	score := 0

	switch {
	case rsiV < 30:
		votes["RSI"] = "BUY"
		score++
	case rsiV > 70:
		votes["RSI"] = "SELL"
		score--
	default:
		votes["RSI"] = "HOLD"
	}

	switch {
	case macdHist > 0:
		votes["MACD"] = "BUY"
		score++
	case macdHist < 0:
		votes["MACD"] = "SELL"
		score--
	default:
		votes["MACD"] = "HOLD"
	}

	switch {
	case price < bollLow:
		votes["BOLL"] = "BUY"
		score++
	case price > bollUp:
		votes["BOLL"] = "SELL"
		score--
	default:
		votes["BOLL"] = "HOLD"
	}

	switch {
	case price > emaV:
		votes["TREND"] = "BUY"
		score++
	case price < emaV:
		votes["TREND"] = "SELL"
		score--
	default:
		votes["TREND"] = "HOLD"
	}

	signal := "HOLD"
	if score >= 2 {
		signal = "BUY"
	} else if score <= -2 {
		signal = "SELL"
	}
	return votes, signal
}

// ─────────────────────────────────────────────────────────────────────────────
//  FORECAST  (honest, illustrative — NOT a guarantee)
//  We blend two simple ideas: momentum (continue the recent EMA slope) and
//  mean-reversion (drift back toward the Bollinger middle). The confidence band
//  widens with the square root of the horizon, exactly because uncertainty
//  grows the further ahead you look. This is here to TEACH that the future is
//  a cone of possibilities, not a single line.
// ─────────────────────────────────────────────────────────────────────────────

func buildForecast(prices []float64, steps int) []ChartPoint {
	n := len(prices)
	if n < 5 {
		return nil
	}
	cur := prices[n-1]
	e := emaSeries(prices, 10)
	slope := 0.0
	if n >= 4 {
		slope = (e[n-1] - e[n-4]) / 3.0 // average EMA change per step
	}
	mid := sma(prices, 20)
	vol := stddev(prices, 14)

	out := make([]ChartPoint, 0, steps)
	p := cur
	for i := 1; i <= steps; i++ {
		revert := (mid - p) * 0.15
		p = p + slope*0.6 + revert
		if p < 0.20 {
			p = 0.20
		}
		band := vol * math.Sqrt(float64(i)) // uncertainty grows with horizon
		up := round2(p + band)
		lo := round2(p - band)
		if lo < 0.05 {
			lo = 0.05
		}
		out = append(out, ChartPoint{Price: round2(p), IsPredict: true, Upper: &up, Lower: &lo})
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
//  HTTP HANDLERS
// ─────────────────────────────────────────────────────────────────────────────

func handleTick(w http.ResponseWriter, r *http.Request) {
	if setupCORS(&w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	mu.Lock()
	resp := snapshot
	mu.Unlock()
	json.NewEncoder(w).Encode(resp)
}

func handleMarketData(w http.ResponseWriter, r *http.Request) {
	if setupCORS(&w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	mu.Lock()
	out := make([]ChartPoint, 0, len(history)+len(prediction))
	out = append(out, history...)
	// connect the forecast to the last real point so the dashed line starts there
	if len(history) > 0 && len(prediction) > 0 {
		last := history[len(history)-1].Price
		out = append(out, ChartPoint{Price: last, IsPredict: true, Upper: &last, Lower: &last})
	}
	out = append(out, prediction...)
	mu.Unlock()
	json.NewEncoder(w).Encode(out)
}

// ─────────────────────────────────────────────────────────────────────────────
//  HELPERS
// ─────────────────────────────────────────────────────────────────────────────

func realPrices() []float64 {
	out := make([]float64, len(history))
	for i, p := range history {
		out[i] = p.Price
	}
	return out
}

func lastF(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
func round4(x float64) float64 { return math.Round(x*10000) / 10000 }
