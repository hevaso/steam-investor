package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

type ChartPoint struct {
	Price     float64 `json:"price"`
	IsPredict bool    `json:"is_predict"`
}

type TickResponse struct {
	Price  float64 `json:"price"`
	Signal string  `json:"signal"`
}

var (
	priceHistory  []float64
	chartData     []ChartPoint
	currentPrice  float64 = 2.95
	currentSignal string  = "HOLD"
	mu            sync.Mutex
)

func setupCORS(w *http.ResponseWriter, r *http.Request) bool {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	(*w).Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		(*w).WriteHeader(http.StatusOK)
		return true
	}
	return false
}

func main() {
	rand.Seed(time.Now().UnixNano())

	for i := 0; i < 30; i++ {
		simulateMarketMatch()
	}

	go runAdvancedMarket()

	http.HandleFunc("/api/market-data", handleMarketData)
	http.HandleFunc("/api/tick", handleTick)

	fmt.Println("[ENGINE] Истински пазарен двигател (RSI + Volume) на порт :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

func runAdvancedMarket() {
	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()

	for {
		<-ticker.C
		mu.Lock()

		simulateMarketMatch()
		rsi := calculateRSI(14)

		if rsi <= 30 {
			currentSignal = "BUY (OVERSOLD)"
		} else if rsi >= 70 {
			currentSignal = "SELL (OVERBOUGHT)"
		} else {
			if len(priceHistory) >= 5 {
				var sum float64
				for _, p := range priceHistory[len(priceHistory)-5:] {
					sum += p
				}
				sma5 := sum / 5.0
				if currentPrice < sma5 {
					currentSignal = "BUY"
				} else {
					currentSignal = "SELL"
				}
			}
		}

		chartData = []ChartPoint{}
		for _, p := range priceHistory {
			chartData = append(chartData, ChartPoint{Price: p, IsPredict: false})
		}

		predictedPrice := currentPrice
		if rsi <= 30 {
			predictedPrice += 0.15
		} else if rsi >= 70 {
			predictedPrice -= 0.15
		} else {
			predictedPrice += (rand.Float64() * 0.06) - 0.03
		}

		chartData = append(chartData, ChartPoint{Price: predictedPrice, IsPredict: true})

		if len(priceHistory) > 40 {
			priceHistory = priceHistory[1:]
		}

		mu.Unlock()
	}
}

func simulateMarketMatch() {
	buyersVolume := rand.Intn(100) + 20
	sellersVolume := rand.Intn(100) + 20

	if buyersVolume > sellersVolume+15 {
		currentPrice += rand.Float64() * 0.08
	} else if sellersVolume > buyersVolume+15 {
		currentPrice -= rand.Float64() * 0.08
	} else {
		currentPrice += (rand.Float64() * 0.02) - 0.01
	}

	if currentPrice < 0.10 {
		currentPrice = 0.10
	}
	priceHistory = append(priceHistory, currentPrice)
}

func calculateRSI(period int) float64 {
	if len(priceHistory) < period+1 {
		return 50.0
	}
	var gains, losses float64
	for i := len(priceHistory) - period; i < len(priceHistory); i++ {
		change := priceHistory[i] - priceHistory[i-1]
		if change > 0 { gains += change } else { losses -= change }
	}
	if losses == 0 { return 100.0 }
	rs := gains / losses
	return 100.0 - (100.0 / (1.0 + rs))
}

func handleTick(w http.ResponseWriter, r *http.Request) {
	if setupCORS(&w, r) { return }
	w.Header().Set("Content-Type", "application/json")
	mu.Lock()
	resp := TickResponse{Price: currentPrice, Signal: currentSignal}
	mu.Unlock()
	json.NewEncoder(w).Encode(resp)
}

func handleMarketData(w http.ResponseWriter, r *http.Request) {
	if setupCORS(&w, r) { return }
	w.Header().Set("Content-Type", "application/json")
	mu.Lock()
	data := make([]ChartPoint, len(chartData))
	copy(data, chartData)
	mu.Unlock()
	json.NewEncoder(w).Encode(data)
}
