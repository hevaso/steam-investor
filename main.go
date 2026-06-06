package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"os"
)

const (
	useRealSteam  = true
	steamAppID    = "730"
	steamItemName = "G18BD283004"
	steamCurrency = "3"
	steamPollSec  = 30
	simStartPrice = 1.50
)

type ChartPoint struct {
	Price     float64  `json:"price"`
	IsPredict bool     `json:"is_predict"`
	Upper     *float64 `json:"upper,omitempty"`
	Lower     *float64 `json:"lower,omitempty"`
}

type TickResponse struct {
	Price    float64           `json:"price"`
	Signal   string            `json:"signal"`
	Mode     string            `json:"mode"`
	Rsi      float64           `json:"rsi"`
	Sma      float64           `json:"sma"`
	Ema      float64           `json:"ema"`
	MacdLine float64           `json:"macd"`
	MacdSig  float64           `json:"macd_signal"`
	MacdHist float64           `json:"macd_hist"`
	BollUp   float64           `json:"boll_upper"`
	BollMid  float64           `json:"boll_mid"`
	BollLow  float64           `json:"boll_lower"`
	Votes    map[string]string `json:"votes"`
	Note     string            `json:"note"`
}

var (
	history      []ChartPoint
	prediction   []ChartPoint
	snapshot     TickResponse
	mu           sync.Mutex
	currentPrice float64 = simStartPrice
	basePrice    float64 = simStartPrice
)

const (
	maxHistory  = 90
	forecastLen = 8
	simTick     = 3000 * time.Millisecond
)

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
	if useRealSteam {
		fmt.Println("[SYSTEM] РЕАЛЕН режим: теглене на цена от Steam за", steamItemName)
		price, lowest, median, vol, err := fetchSteamPrice()
		if err != nil {
			fmt.Println("[STEAM] ВНИМАНИЕ: началната заявка се провали:", err)
			fmt.Println("[STEAM] Провери steamItemName (частта от URL след /730/) и че имаш интернет.")
		} else if price > 0 {
			currentPrice = price
			basePrice = price
			history = append(history, ChartPoint{Price: round2(price), IsPredict: false})
			fmt.Printf("[STEAM] OK -> цена=%.2f EUR (lowest=%.2f median=%.2f) обем=%s\n", price, lowest, median, vol)
		} else {
			fmt.Println("[STEAM] ВНИМАНИЕ: Steam не върна цена (нито lowest, нито median).")
		}
		go runRealEngine()
	} else {
		fmt.Println("[SYSTEM] СИМУЛАЦИОНЕН режим (изкуствени данни).")
		generateInitialHistory(60)
		go runMarketEngine()
	}
	recompute()
	http.HandleFunc("/api/market-data", handleMarketData)
	http.HandleFunc("/api/tick", handleTick)
	http.HandleFunc("/api/trade", handleTrade)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
    	http.ServeFile(w, r, "index.html")
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
	fmt.Println("[SYSTEM] Сървърът е пуснат успешно на :8080")
}

func choosePrice(lowest, median float64) float64 {
	if lowest > 0 {
		return lowest
	}
	return median
}

func fetchSteamPrice() (price, lowest, median float64, volume string, err error) {
	endpoint := fmt.Sprintf("https://steamcommunity.com/market/priceoverview/?appid=%s&currency=%s&market_hash_name=%s",
		steamAppID, steamCurrency, url.QueryEscape(steamItemName))
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, e := client.Do(req)
	if e != nil {
		return 0, 0, 0, "", e
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return 0, 0, 0, "", fmt.Errorf("steam HTTP %d (вероятно rate-limit, изчакай малко)", resp.StatusCode)
	}
	var sp struct {
		Success     bool   `json:"success"`
		LowestPrice string `json:"lowest_price"`
		MedianPrice string `json:"median_price"`
		Volume      string `json:"volume"`
	}
	if e := json.Unmarshal(body, &sp); e != nil {
		return 0, 0, 0, "", e
	}
	if !sp.Success {
		return 0, 0, 0, "", fmt.Errorf("success=false (грешно market_hash_name?)")
	}
	lowest = parsePrice(sp.LowestPrice)
	median = parsePrice(sp.MedianPrice)
	return choosePrice(lowest, median), lowest, median, sp.Volume, nil
}

func parsePrice(s string) float64 {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == ',' || r == '.' {
			b.WriteRune(r)
		}
	}
	t := b.String()
	if t == "" {
		return 0
	}
	lastComma := strings.LastIndex(t, ",")
	lastDot := strings.LastIndex(t, ".")
	if lastComma > lastDot {
		t = strings.ReplaceAll(t, ".", "")
		t = strings.Replace(t, ",", ".", 1)
		t = strings.ReplaceAll(t, ",", "")
	} else {
		t = strings.ReplaceAll(t, ",", "")
	}
	f, _ := strconv.ParseFloat(t, 64)
	return f
}

func runRealEngine() {
	for {
		time.Sleep(time.Duration(steamPollSec) * time.Second)
		price, _, _, _, err := fetchSteamPrice()
		if err != nil {
			fmt.Println("[STEAM] Грешка при заявка:", err)
			continue
		}
		if price > 0 {
			mu.Lock()
			currentPrice = price
			history = append(history, ChartPoint{Price: round2(price), IsPredict: false})
			if len(history) > maxHistory {
				history = history[len(history)-maxHistory:]
			}
			recompute()
			mu.Unlock()
		}
	}
}

func runMarketEngine() {
	ticker := time.NewTicker(simTick)
	defer ticker.Stop()
	for {
		<-ticker.C
		mu.Lock()
		stepPrice()
		recompute()
		mu.Unlock()
	}
}

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

func recompute() {
	prices := realPrices()
	rsiV := rsi(prices, 14)
	smaV := sma(prices, 20)
	emaV := lastF(emaSeries(prices, 20))
	macdLine, macdSig, macdHist := macd(prices)
	bMid, bUp, bLow := bollinger(prices, 20, 2.0)
	votes, signal := decideSignal(round2(currentPrice), rsiV, macdHist, bLow, bUp, emaV)
	mode := "sim"
	note := "Симулирана среда (изкуствени данни). Индикаторите описват миналото; не предсказват бъдещето."
	if useRealSteam {
		mode = "real"
		note = "Реална цена от Steam, обновявана периодично. Индикаторите се нуждаят от достатъчно реални точки."
	}
	snapshot = TickResponse{
		Price:    round2(currentPrice),
		Signal:   signal,
		Mode:     mode,
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
		Note:     note,
	}
	prediction = buildForecast(prices, forecastLen)
}

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

func rsi(prices []float64, period int) float64 {
	if len(prices) <= period {
		return 50.0
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

func buildForecast(prices []float64, steps int) []ChartPoint {
	n := len(prices)
	if n < 5 {
		return nil
	}
	cur := prices[n-1]
	e := emaSeries(prices, 10)
	slope := 0.0
	if n >= 4 {
		slope = (e[n-1] - e[n-4]) / 3.0
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
		band := vol * math.Sqrt(float64(i))
		up := round2(p + band)
		lo := round2(p - band)
		if lo < 0.05 {
			lo = 0.05
		}
		out = append(out, ChartPoint{Price: round2(p), IsPredict: true, Upper: &up, Lower: &lo})
	}
	return out
}

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
	out := make([]ChartPoint, 0, len(history)+len(prediction)+1)
	out = append(out, history...)
	if len(history) > 0 && len(prediction) > 0 {
		last := history[len(history)-1].Price
		out = append(out, ChartPoint{Price: last, IsPredict: true, Upper: &last, Lower: &last})
	}
	out = append(out, prediction...)
	mu.Unlock()
	json.NewEncoder(w).Encode(out)
}

func handleTrade(w http.ResponseWriter, r *http.Request) {
	if setupCORS(&w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	action := r.URL.Query().Get("action")
	mu.Lock()
	if !useRealSteam {
		switch action {
		case "buy":
			currentPrice += currentPrice * 0.004
		case "sell":
			currentPrice -= currentPrice * 0.004
		}
		if currentPrice < 0.20 {
			currentPrice = 0.20
		}
		recompute()
	}
	resp := snapshot
	mu.Unlock()
	json.NewEncoder(w).Encode(resp)
}

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
