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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	useRealSteam     = true
	steamAppID       = "730"
	steamCurrency    = "3"
	steamPollSec     = 30
	steamLoginSecure = ""
	simStartPrice    = 1.50
	maxHistory       = 90
	forecastLen      = 8
	simTick          = 3000 * time.Millisecond
)

var itemConfig = []struct {
	ID, Name, HashName string
	SimBase            float64
}{
	{"dead-hand", "Sealed Dead Hand Terminal", "G18BD283004", 1.50},
	{"kilowatt", "Kilowatt Case", "Kilowatt Case", 0.90},
	{"revolution", "Revolution Case", "Revolution Case", 0.35},
	{"dreams", "Dreams & Nightmares Case", "Dreams & Nightmares Case", 1.30},
}

type ChartPoint struct {
	Price     float64  `json:"price"`
	IsPredict bool     `json:"is_predict"`
	Upper     *float64 `json:"upper,omitempty"`
	Lower     *float64 `json:"lower,omitempty"`
}

type TickResponse struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Price    float64           `json:"price"`
	Signal   string            `json:"signal"`
	Mode     string            `json:"mode"`
	Live     bool              `json:"live"`
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

type ItemInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Asset struct {
	ID         string
	Name       string
	HashName   string
	simBase    float64
	mu         sync.RWMutex
	price      float64
	history    []ChartPoint
	prediction []ChartPoint
	snapshot   TickResponse
	gotReal    bool
}

var (
	assets = map[string]*Asset{}
	order  []string
)

func main() {
	rand.Seed(time.Now().UnixNano())

	for _, c := range itemConfig {
		base := c.SimBase
		if base <= 0 {
			base = simStartPrice
		}
		a := &Asset{
			ID:       c.ID,
			Name:     c.Name,
			HashName: c.HashName,
			simBase:  base,
			price:    base,
		}
		a.history = append(a.history, ChartPoint{Price: round2(base)})
		recomputeAsset(a)
		assets[c.ID] = a
		order = append(order, c.ID)
	}

	if useRealSteam {
		fmt.Println("[SYSTEM] РЕАЛЕН режим. Зареждане на цени от Steam...")
		if steamLoginSecure == "" {
			fmt.Println("[SYSTEM] Без login cookie: 1 точка/артикул в началото, трупам на живо. Индикаторите ще са неутрални, докато се натрупат данни.")
		}
		go func() {
			for _, id := range order {
				seedAssetFromSteam(assets[id])
				time.Sleep(1500 * time.Millisecond)
			}
		}()
		go runRealEngine()
	} else {
		fmt.Println("[SYSTEM] СИМУЛАЦИОНЕН режим (изкуствени данни).")
		for _, id := range order {
			a := assets[id]
			generateInitialHistory(a, 60)
			recomputeAsset(a)
		}
		go runMarketEngine()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", wrap(handleStatic))
	mux.HandleFunc("/api/items", wrap(handleItems))
	mux.HandleFunc("/api/all-ticks", wrap(handleAllTicks))
	mux.HandleFunc("/api/tick", wrap(handleTick))
	mux.HandleFunc("/api/market-data", wrap(handleMarketData))
	mux.HandleFunc("/api/trade", wrap(handleTrade))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 20 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	fmt.Println("[SYSTEM] Сървърът е пуснат успешно на порт " + port)
	log.Fatal(srv.ListenAndServe())
}

func wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[RECOVER] заявка %s предизвика паника: %v", r.URL.Path, rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

func handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "/index.html" {
		http.ServeFile(w, r, "index.html")
		return
	}
	http.NotFound(w, r)
}

func seedAssetFromSteam(a *Asset) {
	var points []ChartPoint
	var newPrice float64

	if steamLoginSecure != "" {
		hist, err := fetchSteamHistory(a.HashName)
		if err == nil && len(hist) >= 5 {
			points = make([]ChartPoint, 0, len(hist))
			for _, pr := range hist {
				points = append(points, ChartPoint{Price: pr})
			}
			newPrice = hist[len(hist)-1]
			fmt.Printf("[STEAM] %s -> история: %d точки (последна %.2f EUR)\n", a.Name, len(hist), newPrice)
		} else if err != nil {
			fmt.Printf("[STEAM] %s -> история неуспешна (%v); пробвам текуща цена\n", a.Name, err)
		}
	}

	if len(points) == 0 {
		price, lowest, median, vol, err := fetchSteamPrice(a.HashName)
		if err != nil {
			fmt.Printf("[STEAM] %s -> ГРЕШКА: %v\n", a.Name, err)
		} else if price > 0 {
			newPrice = price
			points = append(points, ChartPoint{Price: round2(price)})
			fmt.Printf("[STEAM] %s -> %.2f EUR (lowest=%.2f median=%.2f обем=%s)\n", a.Name, price, lowest, median, vol)
		}
	}

	if len(points) > 0 {
		a.mu.Lock()
		a.history = points
		a.price = newPrice
		a.gotReal = true
		recomputeAsset(a)
		a.mu.Unlock()
	}
}

func choosePrice(lowest, median float64) float64 {
	if lowest > 0 {
		return lowest
	}
	return median
}

func fetchSteamPrice(hashName string) (price, lowest, median float64, volume string, err error) {
	endpoint := fmt.Sprintf("https://steamcommunity.com/market/priceoverview/?appid=%s&currency=%s&market_hash_name=%s",
		steamAppID, steamCurrency, url.QueryEscape(hashName))
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
		return 0, 0, 0, "", fmt.Errorf("HTTP %d (вероятно rate-limit)", resp.StatusCode)
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

func fetchSteamHistory(hashName string) ([]float64, error) {
	endpoint := fmt.Sprintf("https://steamcommunity.com/market/pricehistory/?appid=%s&currency=%s&market_hash_name=%s",
		steamAppID, steamCurrency, url.QueryEscape(hashName))
	client := &http.Client{Timeout: 12 * time.Second}
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Cookie", "steamLoginSecure="+steamLoginSecure)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d (cookie невалиден/изтекъл или rate-limit)", resp.StatusCode)
	}
	var ph struct {
		Success bool            `json:"success"`
		Prices  [][]interface{} `json:"prices"`
	}
	if err := json.Unmarshal(body, &ph); err != nil {
		return nil, fmt.Errorf("невалиден отговор (cookie?)")
	}
	if !ph.Success {
		return nil, fmt.Errorf("success=false (cookie невалиден/изтекъл?)")
	}
	out := make([]float64, 0, len(ph.Prices))
	for _, p := range ph.Prices {
		if len(p) >= 2 {
			if f, ok := p[1].(float64); ok {
				out = append(out, round2(f))
			}
		}
	}
	if len(out) > maxHistory {
		out = out[len(out)-maxHistory:]
	}
	return out, nil
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
	per := time.Duration(steamPollSec) * time.Second / time.Duration(len(order))
	if per < 5*time.Second {
		per = 5 * time.Second
	}
	for {
		for _, id := range order {
			a := assets[id]
			price, _, _, _, err := fetchSteamPrice(a.HashName)
			if err != nil {
				fmt.Printf("[STEAM] %s: %v\n", a.Name, err)
			} else if price > 0 {
				a.mu.Lock()
				a.price = price
				a.history = append(a.history, ChartPoint{Price: round2(price)})
				if len(a.history) > maxHistory {
					a.history = a.history[len(a.history)-maxHistory:]
				}
				a.gotReal = true
				recomputeAsset(a)
				a.mu.Unlock()
			}
			time.Sleep(per)
		}
	}
}

func runMarketEngine() {
	ticker := time.NewTicker(simTick)
	defer ticker.Stop()
	for {
		<-ticker.C
		for _, id := range order {
			a := assets[id]
			a.mu.Lock()
			stepPrice(a)
			recomputeAsset(a)
			a.mu.Unlock()
		}
	}
}

func stepPrice(a *Asset) {
	reversion := (a.simBase - a.price) * 0.02
	noise := (rand.Float64() * 0.12) - 0.06
	a.price += reversion + noise
	if a.price < 0.05 {
		a.price = 0.05
	}
	a.history = append(a.history, ChartPoint{Price: round2(a.price)})
	if len(a.history) > maxHistory {
		a.history = a.history[len(a.history)-maxHistory:]
	}
}

func generateInitialHistory(a *Asset, points int) {
	for i := 0; i < points; i++ {
		reversion := (a.simBase - a.price) * 0.02
		noise := (rand.Float64() * 0.10) - 0.05
		a.price += reversion + noise
		if a.price < 0.05 {
			a.price = 0.05
		}
		a.history = append(a.history, ChartPoint{Price: round2(a.price)})
	}
}

func recomputeAsset(a *Asset) {
	prices := pricesOf(a.history)
	rsiV := rsi(prices, 14)
	smaV := sma(prices, 20)
	emaV := lastF(emaSeries(prices, 20))
	macdLine, macdSig, macdHist := macd(prices)
	bMid, bUp, bLow := bollinger(prices, 20, 2.0)
	votes, signal := decideSignal(round2(a.price), rsiV, macdHist, bLow, bUp, emaV)
	mode := "sim"
	note := "Симулирана среда (изкуствени данни). Индикаторите описват миналото; не предсказват бъдещето."
	if useRealSteam {
		mode = "real"
		note = "Реална цена от Steam. Индикаторите се нуждаят от достатъчно реални точки, за да са смислени."
	}
	a.snapshot = TickResponse{
		ID:       a.ID,
		Name:     a.Name,
		Price:    round2(a.price),
		Signal:   signal,
		Mode:     mode,
		Live:     useRealSteam && a.gotReal,
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
	steps := int(math.Round(float64(len(prices)) * 0.25))
	if steps < 2 {
		steps = 2
	}
	if steps > forecastLen {
		steps = forecastLen
	}
	a.prediction = buildForecast(prices, steps)
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
		if p < 0.02 {
			p = 0.02
		}
		band := vol * math.Sqrt(float64(i))
		up := round2(p + band)
		lo := round2(p - band)
		if lo < 0.02 {
			lo = 0.02
		}
		out = append(out, ChartPoint{Price: round2(p), IsPredict: true, Upper: &up, Lower: &lo})
	}
	return out
}

func getAsset(r *http.Request) *Asset {
	id := r.URL.Query().Get("asset")
	if a, ok := assets[id]; ok {
		return a
	}
	return assets[order[0]]
}

func handleItems(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	list := make([]ItemInfo, 0, len(order))
	for _, id := range order {
		list = append(list, ItemInfo{ID: assets[id].ID, Name: assets[id].Name})
	}
	json.NewEncoder(w).Encode(list)
}

func handleAllTicks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	out := make([]TickResponse, 0, len(order))
	for _, id := range order {
		a := assets[id]
		a.mu.RLock()
		out = append(out, a.snapshot)
		a.mu.RUnlock()
	}
	json.NewEncoder(w).Encode(out)
}

func handleTick(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	a := getAsset(r)
	a.mu.RLock()
	resp := a.snapshot
	a.mu.RUnlock()
	json.NewEncoder(w).Encode(resp)
}

func handleMarketData(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	a := getAsset(r)
	a.mu.RLock()
	out := make([]ChartPoint, 0, len(a.history)+len(a.prediction))
	out = append(out, a.history...)
	out = append(out, a.prediction...)
	a.mu.RUnlock()
	json.NewEncoder(w).Encode(out)
}

func handleTrade(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	a := getAsset(r)
	action := r.URL.Query().Get("action")
	a.mu.Lock()
	if !useRealSteam {
		switch action {
		case "buy":
			a.price += a.price * 0.004
		case "sell":
			a.price -= a.price * 0.004
		}
		if a.price < 0.05 {
			a.price = 0.05
		}
		recomputeAsset(a)
	}
	resp := a.snapshot
	a.mu.Unlock()
	json.NewEncoder(w).Encode(resp)
}

func pricesOf(h []ChartPoint) []float64 {
	out := make([]float64, len(h))
	for i, p := range h {
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
